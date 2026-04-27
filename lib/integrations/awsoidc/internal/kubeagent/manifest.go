// Teleport
// Copyright (C) 2026 Gravitational, Inc.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

// Package kubeagent produces the Kubernetes objects that install the
// teleport-kube-agent Helm chart, without depending on Helm at runtime.
//
// The chart is rendered at build time by internal/gen into zz_generated.go
// as typed Go constructors. Manifests composes those constructors based on
// Options, applying the runtime toggles (enterprise / updater / HA) that
// Helm expresses as template conditionals, and overriding chart-rendered
// placeholder values with the runtime Options.
package kubeagent

//go:generate go run -C ./internal/gen . -chart ../../../../../../../examples/chart/teleport-kube-agent -values ../../testdata/values.yaml -out ../../zz_generated.go

import (
	"strings"

	"github.com/coreos/go-semver/semver"
	"github.com/gravitational/trace"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/gravitational/teleport/api"
	"github.com/gravitational/teleport/lib/utils/teleportassets"
)

// Role names the set of Teleport services the agent runs.
type Role string

const (
	// RoleKube indicates only the Kubernetes role is enabled.
	RoleKube Role = "kube"
	// RoleKubeAppDiscovery indicates the Kubernetes, App and Discovery roles are enabled.
	RoleKubeAppDiscovery Role = "kube,app,discovery"
)

// The placeholders below match the values.yaml placeholders used when
// rendering the chart. These values are replaced at runtime based on
// the Options provided to Manifests.
const (
	placeholderNamespace = "ns-placeholder"
	placeholderProxy     = "proxy-placeholder:443"
	placeholderToken     = "token-placeholder"
	placeholderCluster   = "cluster-placeholder"
	placeholderChannel   = "channel-placeholder"
)

// Options configure the teleport-kube-agent chart.
type Options struct {
	Namespace       string
	ProxyAddr       string
	AuthToken       string
	KubeClusterName string
	Roles           Role

	Enterprise bool

	Updater        bool
	UpdaterChannel string

	HighAvailability bool

	RequestedVersion *semver.Version

	Labels map[string]string
}

func (o Options) validate() error {
	switch {
	case o.Namespace == "":
		return trace.BadParameter("Namespace is required")
	case o.ProxyAddr == "":
		return trace.BadParameter("ProxyAddr is required")
	case o.AuthToken == "":
		return trace.BadParameter("AuthToken is required")
	case o.KubeClusterName == "":
		return trace.BadParameter("KubeClusterName is required")
	case o.Roles != RoleKube && o.Roles != RoleKubeAppDiscovery:
		return trace.BadParameter("Roles must be %q or %q, got %q", RoleKube, RoleKubeAppDiscovery, o.Roles)
	case o.Updater && o.UpdaterChannel == "":
		return trace.BadParameter("UpdaterChannel is required when Updater is true")
	}
	return nil
}

// Manifests returns the set of Kubernetes objects derived from the
// teleport-kube-agent with opts applied.
func Manifests(opts Options) ([]client.Object, error) {
	if err := opts.validate(); err != nil {
		return nil, trace.Wrap(err)
	}

	sa := genServiceAccount(opts)
	role := genRole(opts)
	rb := genRoleBinding(opts)
	cr := genClusterRole(opts)
	crb := genClusterRoleBinding(opts)
	cm := genConfigMap(opts)
	sec := genSecret(opts)
	sts := genStatefulSet(opts)

	objs := []client.Object{sa, role, rb, cr, crb, cm, sec, sts}

	if opts.HighAvailability {
		objs = append(objs, genPodDisruptionBudget(opts))
	}

	var updaterRoleBinding *rbacv1.RoleBinding
	var updaterDeployment *appsv1.Deployment
	if opts.Updater {
		updaterRoleBinding = genUpdaterRoleBinding(opts)
		updaterDeployment = genUpdaterDeployment(opts)
		objs = append(objs,
			genUpdaterServiceAccount(opts),
			genUpdaterRole(opts),
			updaterRoleBinding,
			updaterDeployment,
		)
	}

	// Override the namespace placeholder. Cluster-scoped resources
	// (ClusterRole, ClusterRoleBinding) don't have one so leave it empty.
	for _, o := range objs {
		if o.GetNamespace() != "" {
			o.SetNamespace(opts.Namespace)
		}
	}
	setSubjectNamespace(rb.Subjects, opts.Namespace)
	setSubjectNamespace(crb.Subjects, opts.Namespace)
	if updaterRoleBinding != nil {
		setSubjectNamespace(updaterRoleBinding.Subjects, opts.Namespace)
	}

	// Override the auth token placeholder. The chart writes this field
	// as a literal block scalar with a trailing newline. Mirror that so
	// the rendered Secret matches what the chart would produce.
	sec.StringData["auth-token"] = opts.AuthToken + "\n"

	// Override the teleport config.
	teleportYAML, err := applyConfigSubs(teleportConfig(opts.Roles), opts)
	if err != nil {
		return nil, trace.Wrap(err, "applying config substitutions")
	}
	cm.Data = map[string]string{"teleport.yaml": teleportYAML}

	// Override the agent's container image. RequestedVersion takes precedence.
	// The agent image (and embedded version env vars) are rebuilt via
	// teleportassets, which also picks production vs staging ECR by the
	// version's prerelease state. Otherwise, the chart's built-in image
	// is flipped to OSS when Enterprise is false.
	c := &sts.Spec.Template.Spec.Containers[0]
	switch {
	case opts.RequestedVersion != nil:
		c.Image = teleportassets.DistrolessImage(*opts.RequestedVersion)
		// The chart embeds the build-time version in env vars (e.g.
		// TELEPORT_EXT_UPGRADER_VERSION) for self-reporting; rewrite
		// those to match the override.
		newTag := opts.RequestedVersion.String()
		for i := range c.Env {
			c.Env[i].Value = strings.ReplaceAll(c.Env[i].Value, api.Version, newTag)
		}
	case !opts.Enterprise:
		c.Image = strings.ReplaceAll(c.Image, "teleport-ent-distroless", "teleport-distroless")
	}

	// Override the updater placeholders, image, and --base-image arg.
	if updaterDeployment != nil {
		customizeUpdaterArgs(updaterDeployment, opts)
		if opts.RequestedVersion != nil {
			uc := &updaterDeployment.Spec.Template.Spec.Containers[0]
			uc.Image = teleportassets.KubeAgentUpdaterImage(*opts.RequestedVersion)
			// The updater's --base-image arg holds the agent image's repo
			// (registry + name, no tag). The updater appends a channel
			// fetched tag at reconcile time. Use the same agent image
			// teleportassets just constructed and strip its tag.
			agent := teleportassets.DistrolessImage(*opts.RequestedVersion)
			if i := strings.LastIndex(agent, ":"); i >= 0 {
				rewriteBaseImageArg(uc, agent[:i])
			}
		}
	}

	return objs, nil
}

// rewriteBaseImageArg replaces the value of any `--base-image=<...>` arg in
// c.Args with `--base-image=<base>`. The chart writes this arg without a
// tag; the updater appends a channel-derived tag itself.
func rewriteBaseImageArg(c *corev1.Container, base string) {
	const prefix = "--base-image="
	for i, arg := range c.Args {
		if strings.HasPrefix(arg, prefix) {
			c.Args[i] = prefix + base
		}
	}
}

// teleportConfig selects the inner teleport.yaml variant for the given
// roles. The generator emits both variants as constants.
func teleportConfig(r Role) string {
	if r == RoleKubeAppDiscovery {
		return teleportConfigKubeAppDiscovery
	}
	return teleportConfigKube
}

// applyConfigSubs substitutes placeholder values inside the rendered
// teleport.yaml payload and, when Labels is non-empty, injects them into
// kubernetes_service.labels (the chart's destination for static labels on
// the registered kube_cluster resource). Returning a YAML round-trip costs
// us the chart's original key ordering but matches what the chart's
// _config.tpl produced before this package replaced helm.
func applyConfigSubs(payload string, opts Options) (string, error) {
	payload = strings.NewReplacer(
		placeholderProxy, opts.ProxyAddr,
		placeholderToken, opts.AuthToken,
		placeholderCluster, opts.KubeClusterName,
		placeholderNamespace, opts.Namespace,
	).Replace(payload)

	if len(opts.Labels) == 0 {
		return payload, nil
	}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(payload), &doc); err != nil {
		return "", trace.Wrap(err, "parsing rendered teleport.yaml")
	}
	ks, _ := doc["kubernetes_service"].(map[string]any)
	if ks == nil {
		// kubernetes_service should always be present in the rendered
		// config. If it isn't, then we have nowhere to attach static labels.
		return payload, nil
	}
	ks["labels"] = opts.Labels
	doc["kubernetes_service"] = ks

	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", trace.Wrap(err, "serialising teleport.yaml with labels")
	}
	return string(out), nil
}

// setSubjectNamespace overwrites the Namespace of every ServiceAccount
// subject in subs.
func setSubjectNamespace(subs []rbacv1.Subject, ns string) {
	for i := range subs {
		if subs[i].Kind == "ServiceAccount" {
			subs[i].Namespace = ns
		}
	}
}

// customizeUpdaterArgs walks the updater container's args and replaces
// every placeholder string with the corresponding runtime value. Also swaps
// the --base-image arg from the enterprise repo to OSS when
// opts.Enterprise is false, mirroring the StatefulSet image swap.
func customizeUpdaterArgs(d *appsv1.Deployment, opts Options) {
	replacer := strings.NewReplacer(
		placeholderProxy, opts.ProxyAddr,
		placeholderNamespace, opts.Namespace,
		placeholderChannel, opts.UpdaterChannel,
	)
	args := d.Spec.Template.Spec.Containers[0].Args
	for i, a := range args {
		a = replacer.Replace(a)
		if !opts.Enterprise {
			a = strings.ReplaceAll(a, "teleport-ent-distroless", "teleport-distroless")
		}
		args[i] = a
	}
}

// ptr returns a pointer to v. Used by code generation for pointer types (*int32).
func ptr[T any](v T) *T { return &v }

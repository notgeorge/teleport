/*
 * Teleport
 * Copyright (C) 2026  Gravitational, Inc.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package services

import (
	"testing"

	"github.com/stretchr/testify/require"

	scopedaccessv1 "github.com/gravitational/teleport/api/gen/proto/go/teleport/scopes/access/v1"
)

func TestKubeAccessCheckerAdjustDisconnectExpiredCert(t *testing.T) {
	t.Parallel()

	tts := []struct {
		name       string
		spec       *scopedaccessv1.ScopedRoleSpec
		defaultVal bool
		expect     bool
	}{
		{
			name: "unset defers to default false",
			spec: &scopedaccessv1.ScopedRoleSpec{
				Kube: &scopedaccessv1.ScopedRoleKube{},
			},
			defaultVal: false,
			expect:     false,
		},
		{
			name: "unset defers to default true",
			spec: &scopedaccessv1.ScopedRoleSpec{
				Kube: &scopedaccessv1.ScopedRoleKube{},
			},
			defaultVal: true,
			expect:     true,
		},
		{
			name: "explicit true overrides default false",
			spec: &scopedaccessv1.ScopedRoleSpec{
				Kube: &scopedaccessv1.ScopedRoleKube{
					DisconnectExpiredCert: ptr(true),
				},
			},
			defaultVal: false,
			expect:     true,
		},
		{
			name: "explicit false overrides default true",
			spec: &scopedaccessv1.ScopedRoleSpec{
				Kube: &scopedaccessv1.ScopedRoleKube{
					DisconnectExpiredCert: ptr(false),
				},
			},
			defaultVal: true,
			expect:     false,
		},
	}

	for _, tt := range tts {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			checker := newScopedCheckerWithRole(tt.spec).Kube()
			require.Equal(t, tt.expect, checker.AdjustDisconnectExpiredCert(tt.defaultVal))
		})
	}
}

CREATE OR REPLACE PROCEDURE pg_temp.teleport_delete_user(username varchar, admin_user varchar, orphaned_resource_owner varchar, inout state varchar default 'TP003')
LANGUAGE plpgsql
AS $$
BEGIN
    -- Only drop if the user doesn't have other active sessions.
    IF EXISTS (SELECT usename FROM pg_stat_activity WHERE usename = username) THEN
        RAISE NOTICE 'User has active connections';
        RETURN;
    END IF;

    IF orphaned_resource_owner != '' THEN
        -- For REASSIGN OWNED BY to work, admin_user must be a member of both
        -- orphaned_resource_owner and username. For username, we simply GRANT
        -- it to admin_user. For orphaned_resource_owner, admin_user should already
        -- be a member of it, so we attempt REASSIGN OWNED BY, and catch the
        -- insufficient_privilege exception in the event that it fails.
        EXECUTE FORMAT('GRANT %I TO %I', username, admin_user);
        BEGIN
            EXECUTE FORMAT('REASSIGN OWNED BY %I TO %I', username, orphaned_resource_owner);
        EXCEPTION
            WHEN SQLSTATE '42501' THEN
                RAISE WARNING 'admin_user must be a member of orphaned_resource_owner to reassign resources';
        END;
    END IF;

    -- Clean up any dangling grants related to resource reassignment.
    EXECUTE FORMAT('REVOKE %I FROM %I', username, admin_user);

    BEGIN
        EXECUTE FORMAT('DROP USER IF EXISTS %I', username);
    EXCEPTION
        WHEN SQLSTATE '2BP01' THEN
            state := 'TP004';
            -- Drop user/role will fail if user still has dependent objects.
            -- In this scenario, fallback into disabling the user.
            CALL pg_temp.teleport_deactivate_user(username);
    END;
END;$$;

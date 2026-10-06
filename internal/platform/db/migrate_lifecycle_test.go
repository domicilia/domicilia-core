package db_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/domicilia/domicilia-core/internal/platform/db"
)

// 00003_organizations_lifecycle sobre datos como los de staging/producción: hay
// organizaciones con is_active y con el plan 'basic' que puso Alembic.

func TestLifecycleTraspasaLasOrganizacionesExistentes(t *testing.T) {
	url, conn := legacyDB(t)
	if err := db.MigrateUpTo(context.Background(), url, 2, io.Discard); err != nil {
		t.Fatal(err)
	}
	const ts = `timezone('utc', now())`
	exec(t, conn, `INSERT INTO organizations (id, name, slug, is_active, plan_tier, created_at, updated_at) VALUES
		('00000000-0000-4000-8000-0000000000f2', 'Org Dos', 'org-dos', false, 'plan-inventado', `+ts+`, `+ts+`),
		('00000000-0000-4000-8000-0000000000f3', 'Org Tres', 'org-tres', true, 'pro', `+ts+`, `+ts+`)`)

	if err := db.MigrateUpTo(context.Background(), url, 3, io.Discard); err != nil {
		t.Fatalf("aplicar 00003 sobre datos existentes: %v", err)
	}

	state := func(slug string) string {
		return strings.Join(queryStrings(t, conn, `SELECT status || ':' || plan_tier FROM organizations WHERE slug = $1`, slug), "")
	}
	for slug, want := range map[string]string{
		"org-uno":  "active:starter",    // 'basic' pasa a starter
		"org-dos":  "suspended:starter", // is_active = false → suspended; un plan desconocido pasa al más restringido
		"org-tres": "active:pro",        // un plan válido no se toca
	} {
		if got := state(slug); got != want {
			t.Errorf("%s = %q, quería %q", slug, got, want)
		}
	}
	if n := queryStrings(t, conn, `SELECT column_name FROM information_schema.columns WHERE table_name = 'organizations' AND column_name = 'is_active'`); len(n) != 0 {
		t.Error("organizations.is_active debía eliminarse: status es la única fuente de verdad")
	}

	// Cada organización existente queda con sus ajustes y con UNA suscripción vigente
	// igual a su plan.
	if got := queryStrings(t, conn, `SELECT count(*)::text FROM organization_settings`); got[0] != "3" {
		t.Errorf("ajustes = %s, quería 3 (uno por organización)", got[0])
	}
	if got := queryStrings(t, conn, `
		SELECT count(*)::text FROM organizations o
		JOIN organization_subscriptions s ON s.organization_id = o.id AND s.ended_at IS NULL AND s.plan_tier = o.plan_tier`); got[0] != "3" {
		t.Errorf("organizaciones con suscripción vigente igual a su plan = %s, quería 3", got[0])
	}
	if got := queryStrings(t, conn, `SELECT timezone || ' ' || currency FROM organization_settings LIMIT 1`); got[0] != "America/Bogota COP" {
		t.Errorf("valores por omisión de los ajustes = %q", got[0])
	}

	// Los permisos nuevos los tiene el rol admin de sistema (y solo él).
	if got := queryStrings(t, conn, `SELECT string_agg(rp.permission_code, ',' ORDER BY rp.permission_code)
		FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
		WHERE r.code = 'admin' AND r.is_system
		  AND rp.permission_code IN ('org.settings.read', 'org.settings.manage', 'org.billing.read')`); got[0] != "org.billing.read,org.settings.manage,org.settings.read" {
		t.Errorf("permisos nuevos del rol admin = %q", got[0])
	}
	if got := queryStrings(t, conn, `SELECT count(*)::text FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
		WHERE r.code <> 'admin' AND rp.permission_code IN ('org.settings.read', 'org.settings.manage', 'org.billing.read')`); got[0] != "0" {
		t.Errorf("los permisos nuevos no deben ir a otro rol: %s", got[0])
	}

	// La auditoría cambió de nombre sin perder filas ni índices.
	if !tableExists(t, conn, "audit_log") || tableExists(t, conn, "role_audit_log") {
		t.Error("role_audit_log debía pasar a llamarse audit_log")
	}
}

func TestLifecycleRechazaEstadosYPlanesInvalidos(t *testing.T) {
	url, conn := blank(t)
	if _, err := migrate(url, "up", false); err != nil {
		t.Fatal(err)
	}
	const ts = `timezone('utc', now())`
	insert := func(status, plan string) error {
		_, err := conn.Exec(context.Background(), `INSERT INTO organizations (id, name, slug, status, plan_tier, created_at, updated_at)
			VALUES (gen_random_uuid(), gen_random_uuid()::text, gen_random_uuid()::text, $1, $2, `+ts+`, `+ts+`)`, status, plan)
		return err
	}
	if err := insert("active", "starter"); err != nil {
		t.Fatalf("un alta válida falló: %v", err)
	}
	if err := insert("borrada", "starter"); err == nil {
		t.Error("la base debía rechazar un estado desconocido")
	}
	if err := insert("active", "basic"); err == nil {
		t.Error("la base debía rechazar un plan desconocido")
	}
}

func TestLifecycleUnaSolaInvitacionPendientePorCorreo(t *testing.T) {
	url, conn := blank(t)
	if _, err := migrate(url, "up", false); err != nil {
		t.Fatal(err)
	}
	exec(t, conn, `INSERT INTO organizations (id, name, slug, status, plan_tier, created_at, updated_at)
		VALUES ('00000000-0000-4000-8000-0000000000f1', 'O', 'o-slug', 'active', 'starter', now(), now())`)
	roleID := queryStrings(t, conn, `SELECT id::text FROM roles WHERE code = 'employee' AND is_system`)[0]
	invite := func(hash string, extra string) error {
		_, err := conn.Exec(context.Background(), `INSERT INTO organization_invitations
			(organization_id, email, role_id, token_hash, expires_at `+extra+`)
			VALUES ('00000000-0000-4000-8000-0000000000f1', 'a@ejemplo.com', $1, decode($2, 'hex'), now() + interval '1 day')`, roleID, hash)
		return err
	}
	if err := invite("aa", ""); err != nil {
		t.Fatal(err)
	}
	if err := invite("bb", ""); err == nil {
		t.Error("dos invitaciones pendientes al mismo correo debían chocar")
	}
	exec(t, conn, `UPDATE organization_invitations SET revoked_at = now()`)
	if err := invite("cc", ""); err != nil {
		t.Errorf("tras revocar, invitar de nuevo debía poder: %v", err)
	}
}

func TestLifecycleSeRevierte(t *testing.T) {
	url, conn := legacyDB(t)
	if err := db.MigrateUpTo(context.Background(), url, 3, io.Discard); err != nil {
		t.Fatal(err)
	}
	exec(t, conn, `UPDATE organizations SET status = 'suspended' WHERE slug = 'org-uno'`)
	if _, err := migrate(url, "down", false); err != nil {
		t.Fatalf("down de 00003: %v", err)
	}
	if got := queryStrings(t, conn, `SELECT is_active::text FROM organizations WHERE slug = 'org-uno'`); got[0] != "false" {
		t.Errorf("is_active tras la reversa = %s, quería false (estaba suspendida)", got[0])
	}
	for _, name := range []string{"organization_settings", "organization_subscriptions", "organization_feature_overrides", "organization_invitations", "audit_log"} {
		if tableExists(t, conn, name) {
			t.Errorf("la tabla %s debía desaparecer al revertir", name)
		}
	}
	if !tableExists(t, conn, "role_audit_log") {
		t.Error("audit_log debía volver a llamarse role_audit_log")
	}
	// Y un `up` la lleva otra vez a la 3.
	if out, err := migrate(url, "up", false); err != nil || !strings.Contains(out, "00003_organizations_lifecycle.sql") {
		t.Fatalf("up tras la reversa = %q, %v", out, err)
	}
}

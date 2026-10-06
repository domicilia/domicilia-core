package db_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/domicilia/domicilia-core/internal/platform/db"
)

// 00004_whatsapp_inbox: las restricciones que la base impone por sí sola. El código también valida, pero la base es la
// última defensa: si un bug se cuela, estas reglas siguen en pie.

const (
	waOrgA   = "'00000000-0000-4000-8000-0000000000a1'"
	waOrgB   = "'00000000-0000-4000-8000-0000000000b1'"
	waInboxA = "'00000000-0000-4000-8000-0000000001a1'"
	waInboxB = "'00000000-0000-4000-8000-0000000001b1'"
	waCtA    = "'00000000-0000-4000-8000-0000000002a1'"
	waConvA  = "'00000000-0000-4000-8000-0000000003a1'"
)

// whatsappDB deja la base migrada con dos organizaciones, una bandeja y un contacto en A.
func whatsappDB(t *testing.T) func(sql string) error {
	t.Helper()
	url, conn := blank(t)
	if _, err := migrate(url, "up", false); err != nil {
		t.Fatal(err)
	}
	exec(t, conn, `INSERT INTO organizations (id, name, slug, status, plan_tier, created_at, updated_at) VALUES
		(`+waOrgA+`, 'A', 'org-a', 'active', 'starter', now(), now()),
		(`+waOrgB+`, 'B', 'org-b', 'active', 'starter', now(), now())`)
	exec(t, conn, `INSERT INTO inboxes (id, organization_id, name) VALUES
		(`+waInboxA+`, `+waOrgA+`, 'Bandeja A'), (`+waInboxB+`, `+waOrgB+`, 'Bandeja B')`)
	exec(t, conn, `INSERT INTO contacts (id, organization_id, phone_e164, source) VALUES (`+waCtA+`, `+waOrgA+`, '+573001111111', 'manual')`)
	exec(t, conn, `INSERT INTO conversations (id, organization_id, inbox_id, contact_id, display_id)
		VALUES (`+waConvA+`, `+waOrgA+`, `+waInboxA+`, `+waCtA+`, 1)`)
	return func(sql string) error {
		_, err := conn.Exec(context.Background(), sql)
		return err
	}
}

func message(direction, kind, status, extraCols, extraVals string) string {
	return `INSERT INTO messages (organization_id, conversation_id, inbox_id, direction, kind, status` + extraCols + `)
		VALUES (` + waOrgA + `, ` + waConvA + `, ` + waInboxA + `, '` + direction + `', '` + kind + `', '` + status + `'` + extraVals + `)`
}

func TestWhatsAppRestriccionesDeLaBase(t *testing.T) {
	run := whatsappDB(t)

	rejected := []struct{ name, sql string }{
		{"teléfono sin +", `INSERT INTO contacts (organization_id, phone_e164, source) VALUES (` + waOrgA + `, '573001112222', 'manual')`},
		{"teléfono con letras", `INSERT INTO contacts (organization_id, phone_e164, source) VALUES (` + waOrgA + `, '+57abc', 'manual')`},
		{"origen desconocido", `INSERT INTO contacts (organization_id, phone_e164, source) VALUES (` + waOrgA + `, '+573001112222', 'magia')`},
		{"atributos que no son un objeto", `INSERT INTO contacts (organization_id, phone_e164, source, custom_attributes) VALUES (` + waOrgA + `, '+573001112222', 'manual', '[1]')`},
		{"el mismo teléfono en la misma organización", `INSERT INTO contacts (organization_id, phone_e164, source) VALUES (` + waOrgA + `, '+573001111111', 'manual')`},
		{"segunda conversación NO resuelta del mismo contacto", `INSERT INTO conversations (organization_id, inbox_id, contact_id, display_id)
			VALUES (` + waOrgA + `, ` + waInboxA + `, ` + waCtA + `, 2)`},
		{"número de conversación repetido", `INSERT INTO conversations (organization_id, inbox_id, contact_id, display_id, status)
			VALUES (` + waOrgA + `, ` + waInboxA + `, ` + waCtA + `, 1, 'resolved')`},
		{"estado de conversación desconocido", `INSERT INTO conversations (organization_id, inbox_id, contact_id, display_id, status)
			VALUES (` + waOrgA + `, ` + waInboxA + `, ` + waCtA + `, 3, 'borrada')`},
		{"mensaje entrante que no está received", message("inbound", "text", "sent", "", "")},
		{"mensaje saliente received", message("outbound", "text", "received", "", "")},
		{"tipo de mensaje desconocido", message("inbound", "hologram", "received", "", "")},
		{"texto de más de 4096 caracteres", message("inbound", "text", "received", ", body", ", repeat('x', 4097)")},
		{"dos bandejas activas con el mismo nombre (sin distinguir mayúsculas)", `INSERT INTO inboxes (organization_id, name) VALUES (` + waOrgA + `, 'BANDEJA a')`},
		{"tipo de canal desconocido", `INSERT INTO inboxes (organization_id, name, channel_type) VALUES (` + waOrgA + `, 'X', 'telegram')`},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if err := run(tc.sql); err == nil {
				t.Fatal("la base aceptó algo que debía rechazar")
			}
		})
	}

	t.Run("lo permitido sí entra", func(t *testing.T) {
		allowed := map[string]string{
			"el mismo teléfono en OTRA organización":          `INSERT INTO contacts (organization_id, phone_e164, source) VALUES (` + waOrgB + `, '+573001111111', 'inbound')`,
			"el mismo nombre de bandeja en otra organización": `INSERT INTO inboxes (organization_id, name) VALUES (` + waOrgB + `, 'Bandeja A')`,
			"un mensaje entrante bien formado":                message("inbound", "text", "received", ", wamid", ", 'wamid.1'"),
			"un saliente en cola":                             message("outbound", "text", "queued", ", body", ", 'hola'"),
		}
		for name, sql := range allowed {
			if err := run(sql); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
		// El mismo wamid en la misma bandeja NO: es la deduplicación de las reentregas de Meta.
		if err := run(message("inbound", "text", "received", ", wamid", ", 'wamid.1'")); err == nil {
			t.Error("el mismo wamid se insertó dos veces en la misma bandeja")
		}
		// Resuelta la primera, sí puede abrirse otra.
		if err := run(`UPDATE conversations SET status = 'resolved'`); err != nil {
			t.Fatal(err)
		}
		if err := run(`INSERT INTO conversations (organization_id, inbox_id, contact_id, display_id) VALUES (` + waOrgA + `, ` + waInboxA + `, ` + waCtA + `, 2)`); err != nil {
			t.Errorf("con la anterior resuelta debía poder abrirse otra: %v", err)
		}
	})
}

func TestWhatsAppUnNumeroYUnaCuentaSonUnicosEnLaPlataforma(t *testing.T) {
	run := whatsappDB(t)
	const (
		accA = "'00000000-0000-4000-8000-0000000004a1'"
		accB = "'00000000-0000-4000-8000-0000000004b1'"
	)
	if err := run(`INSERT INTO whatsapp_accounts (id, organization_id, waba_id, access_token_enc) VALUES (` + accA + `, ` + waOrgA + `, '111111', 'v1:x')`); err != nil {
		t.Fatal(err)
	}
	// Otra organización no puede reclamar la misma cuenta de Meta.
	if err := run(`INSERT INTO whatsapp_accounts (organization_id, waba_id, access_token_enc) VALUES (` + waOrgB + `, '111111', 'v1:y')`); err == nil {
		t.Error("dos organizaciones conectaron la misma cuenta de WhatsApp Business")
	}
	if err := run(`INSERT INTO whatsapp_channels (inbox_id, organization_id, whatsapp_account_id, phone_number_id, display_phone)
		VALUES (` + waInboxA + `, ` + waOrgA + `, ` + accA + `, '999999', '+57 1')`); err != nil {
		t.Fatal(err)
	}
	if err := run(`INSERT INTO whatsapp_accounts (id, organization_id, waba_id, access_token_enc) VALUES (` + accB + `, ` + waOrgB + `, '222222', 'v1:z')`); err != nil {
		t.Fatal(err)
	}
	// El mismo número no puede estar en dos bandejas: el webhook no sabría a cuál entregar.
	if err := run(`INSERT INTO whatsapp_channels (inbox_id, organization_id, whatsapp_account_id, phone_number_id, display_phone)
		VALUES (` + waInboxB + `, ` + waOrgB + `, ` + accB + `, '999999', '+57 2')`); err == nil {
		t.Error("el mismo phone_number_id quedó en dos bandejas")
	}
	// Un canal no puede usar la cuenta de OTRA organización.
	if err := run(`INSERT INTO whatsapp_channels (inbox_id, organization_id, whatsapp_account_id, phone_number_id, display_phone)
		VALUES (` + waInboxB + `, ` + waOrgB + `, ` + accA + `, '888888', '+57 3')`); err == nil {
		t.Error("un canal de B usó la cuenta de A")
	}
}

func TestWhatsAppSeAplicaSobreDatosExistentesYSeRevierte(t *testing.T) {
	url, conn := legacyDB(t)
	if err := db.MigrateUpTo(context.Background(), url, 3, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateUpTo(context.Background(), url, 4, io.Discard); err != nil {
		t.Fatalf("aplicar 00004 sobre datos existentes: %v", err)
	}
	// Los permisos nuevos quedaron en admin (todos) y en employee (los de atención), y en nadie más.
	got := queryStrings(t, conn, `SELECT r.code || ':' || string_agg(rp.permission_code, ',' ORDER BY rp.permission_code)
		FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
		WHERE rp.permission_code LIKE 'org.inbox.%' OR rp.permission_code LIKE 'org.contacts.%'
		GROUP BY r.code ORDER BY r.code`)
	wantPerms := []string{
		"admin:org.contacts.manage,org.contacts.read,org.inbox.manage,org.inbox.read,org.inbox.reply",
		"employee:org.contacts.manage,org.contacts.read,org.inbox.read,org.inbox.reply",
	}
	if strings.Join(got, "|") != strings.Join(wantPerms, "|") {
		t.Fatalf("permisos = %v", got)
	}
	// Lo anterior sigue intacto.
	if n := queryStrings(t, conn, `SELECT count(*)::text FROM organizations`); n[0] != "1" {
		t.Errorf("organizaciones = %s", n[0])
	}

	if _, err := migrate(url, "down", false); err != nil {
		t.Fatalf("down de 00004: %v", err)
	}
	for _, name := range []string{"inboxes", "contacts", "conversations", "messages", "whatsapp_accounts", "webhook_events"} {
		if tableExists(t, conn, name) {
			t.Errorf("la tabla %s debía desaparecer al revertir", name)
		}
	}
	if n := queryStrings(t, conn, `SELECT count(*)::text FROM permissions WHERE code LIKE 'org.inbox.%' OR code LIKE 'org.contacts.%'`); n[0] != "0" {
		t.Errorf("los permisos nuevos debían desaparecer: %s", n[0])
	}
	if out, err := migrate(url, "up", false); err != nil || !strings.Contains(out, "00004_whatsapp_inbox.sql") {
		t.Fatalf("up tras la reversa = %q, %v", out, err)
	}
}

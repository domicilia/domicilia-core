package app_test

// Arnés de las pruebas de contrato: la aplicación REAL (app.New, la misma que
// arranca main) contra un Postgres REAL con las migraciones reales de goose. Lo
// único simulado es auth-domicilia (GoTrue), que aquí no corre.
//
// Estas pruebas son la especificación de comportamiento heredada de las de
// pytest de domicilia-api: lo que más se prueba es lo que debe RECHAZARse,
// porque un fallo de autorización no produce un error visible, produce que
// alguien vea datos que no le corresponden.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/app"
	"github.com/domicilia/domicilia-core/internal/catalog"
	"github.com/domicilia/domicilia-core/internal/organizations"
	"github.com/domicilia/domicilia-core/internal/platform/config"
	"github.com/domicilia/domicilia-core/internal/platform/gotrue"
	"github.com/domicilia/domicilia-core/internal/testutil/pgtest"
)

const (
	testSecret   = "clave-solo-para-pruebas-no-usar-en-ningun-entorno"
	testAudience = "authenticated"
	testAppURL   = "https://app.ejemplo.com"
)

var (
	pool     *pgxpool.Pool
	startErr error
)

func TestMain(m *testing.M) { os.Exit(run(m)) }

// run levanta UN Postgres para todo el paquete. Si no hay Docker, deja el error
// en startErr y cada prueba decide (pgtest.Skip) entre omitirse o fallar.
func run(m *testing.M) int {
	ctx := context.Background()
	srv, err := pgtest.Start(ctx)
	if err != nil {
		startErr = err
		return m.Run()
	}
	defer srv.Close()

	p, _, err := srv.Migrated(ctx)
	if err != nil {
		startErr = err
		return m.Run()
	}
	defer p.Close()
	pool = p

	return m.Run()
}

// harness es una aplicación recién armada sobre una base vacía.
type harness struct {
	t       *testing.T
	handler *echo.Echo
	idp     *fakeIDP
	meta    *fakeMeta
	epayco  *fakeEpayco
	workers *app.Workers
	// logs es todo lo que la aplicación escribió en su log: las pruebas comprueban que ahí nunca hay secretos.
	logs syncBuffer
	// clock, si no es cero, es la hora "actual" que ve la aplicación. Sin fijarla,
	// corre la real.
	clock time.Time
}

// syncBuffer es un búfer seguro para escribir desde varias goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newHarness arma la aplicación. Las opciones ajustan la configuración (p. ej. sin llave de cifrado).
func newHarness(t *testing.T, opts ...func(*config.Config)) *harness {
	t.Helper()
	pgtest.Skip(t, startErr)
	must(t, pgtest.Reset(context.Background(), pool))

	idp := &fakeIDP{pool: pool}
	h := &harness{t: t, idp: idp, meta: newFakeMeta(t), epayco: newFakeEpayco(t)}
	now := func() time.Time {
		if !h.clock.IsZero() {
			return h.clock
		}
		return time.Now()
	}
	cfg := config.Config{
		Env:               "development",
		JWTSecret:         testSecret,
		JWTAudience:       testAudience,
		AppURL:            testAppURL,
		RequestTimeout:    10 * time.Second,
		MaxBodyBytes:      8 << 20, // mismo techo que producción: la subida de fotos de producto lo necesita
		SecretsKey:        newSecretsKey(t),
		WhatsAppAppSecret: testAppSecret, WhatsAppVerifyToken: testVerifyToken,
		WhatsAppAPIBase: h.meta.srv.URL, WhatsAppAPIVersion: testMetaVersion,
		PublicURL:        testPublicURL,
		EpaycoPublicKey:  testEpaycoPublicKey,
		EpaycoPrivateKey: testEpaycoPrivateKey,
		EpaycoCustomerID: testEpaycoCustomerID,
		EpaycoTestMode:   true,
		EpaycoApifyURL:   h.epayco.srv.URL,
	}
	for _, o := range opts {
		o(&cfg)
	}
	deps := app.Deps{
		Log:      slog.New(slog.NewTextHandler(&h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Config:   cfg,
		Pool:     pool,
		Identity: idp,
		Now:      now,
	}
	h.workers = app.NewWorkers(deps)
	deps.Workers = h.workers
	h.handler = app.New(deps)
	return h
}

// newHarnessWithMedia es newHarness, pero con un catalog.MediaUploader de prueba en vez del real
// (azblob.Client, que solo funciona dentro de una VM de Azure vía IMDS) — así se puede probar el
// flujo completo de subir una foto sin red real.
func newHarnessWithMedia(t *testing.T, media catalog.MediaUploader, opts ...func(*config.Config)) *harness {
	t.Helper()
	pgtest.Skip(t, startErr)
	must(t, pgtest.Reset(context.Background(), pool))

	idp := &fakeIDP{pool: pool}
	h := &harness{t: t, idp: idp, meta: newFakeMeta(t), epayco: newFakeEpayco(t)}
	now := func() time.Time {
		if !h.clock.IsZero() {
			return h.clock
		}
		return time.Now()
	}
	cfg := config.Config{
		Env:               "development",
		JWTSecret:         testSecret,
		JWTAudience:       testAudience,
		AppURL:            testAppURL,
		RequestTimeout:    10 * time.Second,
		MaxBodyBytes:      8 << 20,
		SecretsKey:        newSecretsKey(t),
		WhatsAppAppSecret: testAppSecret, WhatsAppVerifyToken: testVerifyToken,
		WhatsAppAPIBase: h.meta.srv.URL, WhatsAppAPIVersion: testMetaVersion,
		PublicURL:        testPublicURL,
		EpaycoPublicKey:  testEpaycoPublicKey,
		EpaycoPrivateKey: testEpaycoPrivateKey,
		EpaycoCustomerID: testEpaycoCustomerID,
		EpaycoTestMode:   true,
		EpaycoApifyURL:   h.epayco.srv.URL,
	}
	for _, o := range opts {
		o(&cfg)
	}
	deps := app.Deps{
		Log:           slog.New(slog.NewTextHandler(&h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Config:        cfg,
		Pool:          pool,
		Identity:      idp,
		Now:           now,
		MediaUploader: media,
	}
	h.workers = app.NewWorkers(deps)
	deps.Workers = h.workers
	h.handler = app.New(deps)
	return h
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// GoTrue simulado
// ---------------------------------------------------------------------------

// fakeIDP hace lo que el admin API real hace y le importa a la FK de
// public.users: crear una fila en auth.users con un id nuevo.
type fakeIDP struct {
	pool *pgxpool.Pool

	mu        sync.Mutex
	created   []string    // correos con los que se creó una cuenta
	deleted   []uuid.UUID // cuentas que se deshicieron
	createErr error       // si no es nil, CreateUser falla con él
	forceID   *uuid.UUID  // si no es nil, CreateUser devuelve este id (ya existente)
	// accounts son cuentas que ya existen en GoTrue (por correo), con o sin perfil
	// de negocio: CreateUser las rechaza con email_exists, como el admin API real.
	accounts map[string]uuid.UUID
}

func (f *fakeIDP) CreateUser(ctx context.Context, email, _ string) (gotrue.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return gotrue.User{}, f.createErr
	}
	if _, taken := f.accounts[email]; taken {
		return gotrue.User{}, &gotrue.Error{Status: http.StatusUnprocessableEntity, Code: "email_exists", Body: "email_exists"}
	}
	id := uuid.New()
	if f.forceID != nil {
		id = *f.forceID
	} else if _, err := f.pool.Exec(ctx, `INSERT INTO auth.users (id) VALUES ($1)`, id); err != nil {
		return gotrue.User{}, err
	}
	f.created = append(f.created, email)
	return gotrue.User{ID: id, Email: email}, nil
}

func (f *fakeIDP) UserByEmail(_ context.Context, email string) (*gotrue.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.accounts[email]; ok {
		return &gotrue.User{ID: id, Email: email}, nil
	}
	return nil, nil //nolint:nilnil // "no existe" no es un error
}

// DeleteUser borra la identidad solo si es huérfana (sin perfil): imita que en
// producción el perfil de negocio de otra persona no se toca por error.
func (f *fakeIDP) DeleteUser(ctx context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	_, err := f.pool.Exec(ctx,
		`DELETE FROM auth.users WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id)
	return err
}

func (f *fakeIDP) createdCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func (f *fakeIDP) deletedIDs() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uuid.UUID(nil), f.deleted...)
}

// ---------------------------------------------------------------------------
// Datos de prueba
// ---------------------------------------------------------------------------

type person struct {
	ID    uuid.UUID
	Email string
}

type userSpec struct {
	email      string
	superadmin bool
	inactive   bool
	delivery   bool
}

type userOpt func(*userSpec)

func withEmail(e string) userOpt { return func(s *userSpec) { s.email = e } }
func superadmin() userOpt        { return func(s *userSpec) { s.superadmin = true } }
func inactive() userOpt          { return func(s *userSpec) { s.inactive = true } }
func delivery() userOpt          { return func(s *userSpec) { s.delivery = true } }

// user crea un usuario de negocio. La FK exige la fila "de GoTrue" antes que la
// de negocio, igual que en producción.
func (h *harness) user(opts ...userOpt) person {
	h.t.Helper()
	ctx := context.Background()
	spec := userSpec{email: "u-" + uuid.NewString()[:8] + "@ejemplo.com"}
	for _, o := range opts {
		o(&spec)
	}
	id := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO auth.users (id) VALUES ($1)`, id)
	must(h.t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO users (id, email, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, timezone('utc', now()), timezone('utc', now()))`,
		id, spec.email, !spec.inactive)
	must(h.t, err)
	if spec.superadmin {
		h.grant(id, "superadmin")
	}
	if spec.delivery {
		h.grant(id, "delivery")
	}
	return person{ID: id, Email: spec.email}
}

type organization struct {
	ID   uuid.UUID
	Name string
	Slug string
}

// org crea una organización activa.
func (h *harness) org(name string) organization { return h.orgWith(name, true) }

func (h *harness) orgWith(name string, active bool) organization {
	status := "active"
	if !active {
		status = "suspended"
	}
	return h.orgStatus(name, status)
}

// orgStatus crea una organización en un estado dado (active, suspended, archived) y
// con el plan por omisión.
func (h *harness) orgStatus(name, status string) organization {
	h.t.Helper()
	if name == "" {
		name = "Org " + uuid.NewString()[:8]
	}
	o := organization{ID: uuid.New(), Name: name, Slug: organizations.Slugify(name)}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO organizations (id, name, slug, status, plan_tier, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'starter', timezone('utc', now()), timezone('utc', now()))`,
		o.ID, o.Name, o.Slug, status)
	must(h.t, err)
	// Toda organización real (por la API, el seed o la migración) tiene su suscripción
	// vigente: el arnés la refleja para que el historial sea el de verdad.
	_, err = pool.Exec(context.Background(), `INSERT INTO organization_subscriptions (organization_id, plan_tier) VALUES ($1, 'starter')`, o.ID)
	must(h.t, err)
	return o
}

// setPlan cambia el plan de una organización por SQL (sin pasar por la API).
func (h *harness) setPlan(o organization, tier string) {
	h.t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `UPDATE organizations SET plan_tier = $2 WHERE id = $1`, o.ID, tier)
	must(h.t, err)
	_, err = pool.Exec(ctx, `UPDATE organization_subscriptions SET plan_tier = $2 WHERE organization_id = $1 AND ended_at IS NULL`, o.ID, tier)
	must(h.t, err)
}

// join afilia a un usuario a una organización con un rol.
func (h *harness) join(u person, o organization, role string) {
	h.t.Helper()
	// El rol puede ser de sistema o uno personalizado de esa organización.
	tag, err := pool.Exec(context.Background(), `
		INSERT INTO user_organizations (user_id, organization_id, role_id, created_at, updated_at)
		SELECT $1, $2, r.id, timezone('utc', now()), timezone('utc', now())
		FROM roles r
		WHERE r.code = $3 AND r.scope = 'organization' AND (r.organization_id IS NULL OR r.organization_id = $2)
		ORDER BY r.organization_id NULLS LAST LIMIT 1`,
		u.ID, o.ID, role)
	must(h.t, err)
	if tag.RowsAffected() != 1 {
		// Un fixture que inserta cero filas sin avisar hace que las pruebas pasen por
		// el motivo equivocado.
		h.t.Fatalf("join: no existe el rol %q en la organización %s", role, o.Name)
	}
}

// grant da un rol de plataforma de sistema.
func (h *harness) grant(userID uuid.UUID, code string) {
	h.t.Helper()
	tag, err := pool.Exec(context.Background(), `
		INSERT INTO user_platform_roles (user_id, role_id)
		SELECT $1, r.id FROM roles r WHERE r.code = $2 AND r.scope = 'platform' AND r.organization_id IS NULL`,
		userID, code)
	must(h.t, err)
	if tag.RowsAffected() != 1 {
		h.t.Fatalf("grant: no existe el rol de plataforma %q", code)
	}
}

// hasRole dice si el usuario tiene un rol de plataforma.
func hasRole(t *testing.T, userID uuid.UUID, code string) bool {
	t.Helper()
	return flag(t, `SELECT EXISTS (SELECT 1 FROM user_platform_roles upr JOIN roles r ON r.id = upr.role_id
		WHERE upr.user_id = $1 AND r.code = $2 AND r.scope = 'platform')`, userID, code)
}

// hasRoleEmail es hasRole buscando al usuario por correo.
func hasRoleEmail(t *testing.T, email, code string) bool {
	t.Helper()
	return flag(t, `SELECT EXISTS (SELECT 1 FROM user_platform_roles upr JOIN roles r ON r.id = upr.role_id
		JOIN users u ON u.id = upr.user_id WHERE u.email = $1 AND r.code = $2 AND r.scope = 'platform')`, email, code)
}

// authIdentity crea solo la identidad de GoTrue (sin perfil de negocio): el
// estado de quien acaba de registrarse y todavía no tiene perfil.
func (h *harness) authIdentity() uuid.UUID {
	h.t.Helper()
	id := uuid.New()
	_, err := pool.Exec(context.Background(), `INSERT INTO auth.users (id) VALUES ($1)`, id)
	must(h.t, err)
	return id
}

// ---------------------------------------------------------------------------
// Peticiones
// ---------------------------------------------------------------------------

// signToken firma un token con la forma del que emitiría GoTrue. mutate permite
// fabricar variantes inválidas (otra audiencia, caducado...).
func signToken(t *testing.T, key, sub string, mutate func(jwt.MapClaims)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":  sub,
		"aud":  testAudience,
		"role": "authenticated",
		"exp":  time.Now().Add(5 * time.Minute).Unix(),
	}
	if mutate != nil {
		mutate(claims)
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(key))
	must(t, err)
	return s
}

// bearer devuelve el token válido de una persona.
func (h *harness) bearer(p person) string {
	return signToken(h.t, testSecret, p.ID.String(), func(c jwt.MapClaims) { c["email"] = p.Email })
}

// do envía una petición como `as` (nil = sin credenciales).
func (h *harness) do(method, path string, body any, as *person) *httptest.ResponseRecorder {
	h.t.Helper()
	token := ""
	if as != nil {
		token = h.bearer(*as)
	}
	return h.doToken(method, path, body, token)
}

// doToken envía una petición con un token ya armado ("" = sin cabecera).
func (h *harness) doToken(method, path string, body any, token string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			rd = strings.NewReader(s) // cuerpo crudo, para probar JSON mal formado
		} else {
			b, err := json.Marshal(body)
			must(h.t, err)
			rd = bytes.NewReader(b)
		}
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	h.checkContract(req, rec)
	return rec
}

func want(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("código = %d, quería %d\ncuerpo: %s", rec.Code, code, rec.Body.String())
	}
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("respuesta no es JSON válido: %v\ncuerpo: %s", err, rec.Body.String())
	}
	return v
}

// jsonMap decodifica una respuesta como objeto genérico.
func jsonMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	return decode[map[string]any](t, rec)
}

// ---------------------------------------------------------------------------
// Consultas de verificación
// ---------------------------------------------------------------------------

func count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	must(t, pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func str(t *testing.T, query string, args ...any) string {
	t.Helper()
	var s string
	must(t, pool.QueryRow(context.Background(), query, args...).Scan(&s))
	return s
}

func flag(t *testing.T, query string, args ...any) bool {
	t.Helper()
	var b bool
	must(t, pool.QueryRow(context.Background(), query, args...).Scan(&b))
	return b
}

func strs(t *testing.T, query string, args ...any) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), query, args...)
	must(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		must(t, rows.Scan(&s))
		out = append(out, s)
	}
	must(t, rows.Err())
	return out
}

// platformRole crea un rol de plataforma personalizado con esos permisos y se lo da al
// usuario: sirve para probar que un permiso suelto no da más que lo que dice.
func (h *harness) platformRole(u person, perms ...string) {
	h.t.Helper()
	ctx := context.Background()
	code := "rol-" + uuid.NewString()[:8]
	var id uuid.UUID
	must(h.t, pool.QueryRow(ctx, `INSERT INTO roles (code, name, scope) VALUES ($1, $1, 'platform') RETURNING id`, code).Scan(&id))
	for _, p := range perms {
		_, err := pool.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_code) VALUES ($1, $2)`, id, p)
		must(h.t, err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO user_platform_roles (user_id, role_id) VALUES ($1, $2)`, u.ID, id)
	must(h.t, err)
}

func orgURL(o organization, suffix string) string {
	return "/v1/organizations/" + o.ID.String() + suffix
}

func platformOrgURL(o organization, suffix string) string {
	return "/v1/platform/organizations/" + o.ID.String() + suffix
}

// auditActions devuelve las acciones auditadas de una organización, en orden.
func auditActions(t *testing.T, o organization) []string {
	t.Helper()
	return strs(t, `SELECT action FROM audit_log WHERE organization_id = $1 ORDER BY created_at, id`, o.ID)
}

func contains(list []string, item string) bool {
	for _, s := range list {
		if s == item {
			return true
		}
	}
	return false
}

// orgAdmin crea una organización con un administrador (y el plan enterprise, para que
// los límites de miembros no estorben salvo que la prueba los ejerza).
func orgAdmin(h *harness, name string) (person, organization) {
	h.t.Helper()
	admin, o := h.user(), h.org(name)
	h.setPlan(o, "enterprise")
	h.join(admin, o, "admin")
	return admin, o
}

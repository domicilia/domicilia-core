package inboxes

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/meta"
	"github.com/domicilia/domicilia-core/internal/plans"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/secretbox"
	"github.com/domicilia/domicilia-core/internal/platform/validate"
	"github.com/domicilia/domicilia-core/internal/tenant"
)

// idPattern es la forma de los ids de Meta (solo dígitos). Validarla evita que algo raro llegue a
// una URL de la Graph API.
var idPattern = regexp.MustCompile(`^[0-9]{5,32}$`)

// MetaAPI es lo que se usa de Meta aquí.
type MetaAPI interface {
	ListPhoneNumbers(ctx context.Context, token, wabaID string) ([]meta.PhoneNumber, error)
}

// Plans es la puerta de las funciones por plan.
type Plans interface {
	Require(ctx context.Context, orgID uuid.UUID, f plans.Feature) error
}

// Service reúne las reglas de las bandejas.
type Service struct {
	repo  Repository
	gate  *tenant.Gate
	meta  MetaAPI
	box   *secretbox.Box // nil: el cifrado no está configurado y no se puede conectar nada
	plans Plans
	log   *slog.Logger
}

// NewService crea el servicio. box puede ser nil: entonces conectar responde 503 en lugar de
// guardar el token sin cifrar.
func NewService(repo Repository, gate *tenant.Gate, m MetaAPI, box *secretbox.Box, p Plans, log *slog.Logger) *Service {
	return &Service{repo: repo, gate: gate, meta: m, box: box, plans: p, log: log}
}

func tokenAAD(accountID uuid.UUID) string {
	return "whatsapp_account:" + accountID.String() + ":access_token"
}
func secretAAD(accountID uuid.UUID) string {
	return "whatsapp_account:" + accountID.String() + ":app_secret"
}

// ConnectInput son los datos para conectar un número de WhatsApp.
type ConnectInput struct {
	Name          string
	WABAID        string
	PhoneNumberID string
	AccessToken   string
	// AppSecret es el secreto de la app de Meta que firma los webhooks de este número. Solo hace
	// falta si el negocio usa su PROPIA app de Meta; con la app de la plataforma va vacío.
	AppSecret string
}

func (s *Service) requireBox() error {
	if s.box == nil {
		return apperr.Unavailable("conectar WhatsApp no está disponible: falta configurar el cifrado de secretos en el servidor")
	}
	return nil
}

func validateToken(field, tok string) (string, error) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", apperr.Invalid(field + " es obligatorio")
	}
	if len(tok) > maxTokenLength || strings.IndexFunc(tok, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", apperr.Invalid(field + " no es válido")
	}
	return tok, nil
}

// fromMeta traduce un fallo de Meta a un error de negocio. El token nunca forma parte del mensaje.
func fromMeta(err error) error {
	var ae *meta.APIError
	if errors.As(err, &ae) {
		if ae.HTTPStatus >= 500 {
			return apperr.Upstream("Meta no está disponible en este momento", err)
		}
		if ae.AuthFailed() {
			return apperr.Invalid("Meta rechazó el token: " + ae.Message)
		}
		return apperr.Invalid("Meta rechazó la solicitud: " + ae.Message)
	}
	return apperr.Upstream("no se pudo comunicar con Meta", err)
}

// Connect conecta un número. Valida las credenciales CONTRA Meta antes de guardar nada: el número
// debe pertenecer a la cuenta y el token debe poder verlo. El token se guarda cifrado.
func (s *Service) Connect(ctx context.Context, actor identity.Principal, orgID uuid.UUID, in ConnectInput) (Inbox, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxManage, true); err != nil {
		return Inbox{}, err
	}
	if err := s.requireBox(); err != nil {
		return Inbox{}, err
	}
	name, err := validate.Required("name", in.Name, maxName)
	if err != nil {
		return Inbox{}, err
	}
	if !idPattern.MatchString(in.WABAID) {
		return Inbox{}, apperr.Invalid("waba_id debe ser el identificador numérico de la cuenta de WhatsApp Business")
	}
	if !idPattern.MatchString(in.PhoneNumberID) {
		return Inbox{}, apperr.Invalid("phone_number_id debe ser el identificador numérico del número")
	}
	token, err := validateToken("access_token", in.AccessToken)
	if err != nil {
		return Inbox{}, err
	}
	appSecret := strings.TrimSpace(in.AppSecret)
	if appSecret != "" {
		if appSecret, err = validateToken("app_secret", appSecret); err != nil {
			return Inbox{}, err
		}
	}

	// Plan: la primera bandeja es del plan base; tener más de una es de Enterprise.
	if err := s.plans.Require(ctx, orgID, plans.Inbox24h); err != nil {
		return Inbox{}, err
	}
	if n, err := s.repo.CountActive(ctx, orgID); err != nil {
		return Inbox{}, err
	} else if n >= 1 {
		if err := s.plans.Require(ctx, orgID, plans.MultiInbox); err != nil {
			return Inbox{}, err
		}
	}

	numbers, err := s.meta.ListPhoneNumbers(ctx, token, in.WABAID)
	if err != nil {
		return Inbox{}, fromMeta(err)
	}
	var num *meta.PhoneNumber
	for i := range numbers {
		if numbers[i].ID == in.PhoneNumberID {
			num = &numbers[i]
			break
		}
	}
	if num == nil {
		return Inbox{}, apperr.Invalid("ese número no pertenece a esa cuenta de WhatsApp Business")
	}

	accountID, inboxID := uuid.New(), uuid.New()
	tokenEnc, err := s.box.Seal(token, tokenAAD(accountID))
	if err != nil {
		return Inbox{}, err
	}
	n := NewConnection{
		OrganizationID: orgID, InboxID: inboxID, AccountID: accountID, Name: name, WABAID: in.WABAID,
		PhoneNumberID: in.PhoneNumberID, DisplayPhone: num.DisplayPhone, VerifiedName: num.VerifiedName,
		QualityRating: num.QualityRating, MessagingTier: num.MessagingTier, TokenEnc: tokenEnc, Actor: actor.ID,
	}
	if appSecret != "" {
		if n.AppSecretEnc, err = s.box.Seal(appSecret, secretAAD(accountID)); err != nil {
			return Inbox{}, err
		}
	}
	switch err := s.repo.Connect(ctx, n); {
	case errors.Is(err, ErrNameTaken):
		return Inbox{}, apperr.Conflict("ya tienes una bandeja con ese nombre")
	case errors.Is(err, ErrPhoneTaken):
		return Inbox{}, apperr.Conflict("ese número ya está conectado")
	case errors.Is(err, ErrWABATaken):
		return Inbox{}, apperr.Conflict("esa cuenta de WhatsApp Business ya está conectada")
	case err != nil:
		return Inbox{}, err
	}
	return s.repo.Get(ctx, orgID, inboxID)
}

// List devuelve las bandejas. Las archivadas solo las ve quien administra.
func (s *Service) List(ctx context.Context, actor identity.Principal, orgID uuid.UUID, includeArchived bool) ([]Inbox, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxRead, false); err != nil {
		return nil, err
	}
	includeArchived = includeArchived && actor.CanInOrg(orgID, access.OrgInboxManage)
	return s.repo.List(ctx, orgID, includeArchived)
}

// Get devuelve una bandeja.
func (s *Service) Get(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Inbox, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxRead, false); err != nil {
		return Inbox{}, err
	}
	return s.get(ctx, orgID, id)
}

func (s *Service) get(ctx context.Context, orgID, id uuid.UUID) (Inbox, error) {
	in, err := s.repo.Get(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) {
		return Inbox{}, apperr.NotFound("bandeja no encontrada")
	}
	return in, err
}

// Rename cambia el nombre.
func (s *Service) Rename(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID, rawName string) (Inbox, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxManage, true); err != nil {
		return Inbox{}, err
	}
	name, err := validate.Required("name", rawName, maxName)
	if err != nil {
		return Inbox{}, err
	}
	switch err := s.repo.Rename(ctx, orgID, id, name, actor.ID); {
	case errors.Is(err, ErrNotFound):
		return Inbox{}, apperr.NotFound("bandeja no encontrada")
	case errors.Is(err, ErrNameTaken):
		return Inbox{}, apperr.Conflict("ya tienes una bandeja con ese nombre")
	case err != nil:
		return Inbox{}, err
	}
	return s.get(ctx, orgID, id)
}

// Archive desconecta la bandeja: deja de recibir y de enviar, el número queda libre y las
// conversaciones se conservan.
func (s *Service) Archive(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) error {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxManage, true); err != nil {
		return err
	}
	if err := s.repo.Archive(ctx, orgID, id, actor.ID); errors.Is(err, ErrNotFound) {
		return apperr.NotFound("bandeja no encontrada")
	} else if err != nil {
		return err
	}
	return nil
}

// RotateToken cambia el token de la cuenta (el anterior venció o se revocó). Se valida contra
// Meta antes de guardar, con el mismo número de la bandeja.
func (s *Service) RotateToken(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID, rawToken string) (Inbox, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxManage, true); err != nil {
		return Inbox{}, err
	}
	if err := s.requireBox(); err != nil {
		return Inbox{}, err
	}
	token, err := validateToken("access_token", rawToken)
	if err != nil {
		return Inbox{}, err
	}
	stored, err := s.repo.Stored(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) {
		return Inbox{}, apperr.NotFound("bandeja no encontrada")
	}
	if err != nil {
		return Inbox{}, err
	}
	if err := s.checkNumber(ctx, token, stored); err != nil {
		return Inbox{}, err
	}
	enc, err := s.box.Seal(token, tokenAAD(stored.AccountID))
	if err != nil {
		return Inbox{}, err
	}
	if err := s.repo.RotateToken(ctx, orgID, id, stored.AccountID, enc, actor.ID); err != nil {
		return Inbox{}, err
	}
	return s.get(ctx, orgID, id)
}

// checkNumber comprueba que el token ve el número de la bandeja.
func (s *Service) checkNumber(ctx context.Context, token string, stored StoredCredentials) error {
	numbers, err := s.meta.ListPhoneNumbers(ctx, token, stored.WABAID)
	if err != nil {
		return fromMeta(err)
	}
	for _, n := range numbers {
		if n.ID == stored.PhoneNumberID {
			return nil
		}
	}
	return apperr.Invalid("ese token no tiene acceso al número de esta bandeja")
}

// Refresh vuelve a leer de Meta los datos del número (calidad, nivel de mensajería) y comprueba que
// el token siga sirviendo. Si Meta lo rechaza, el canal pasa a needs_reauth.
func (s *Service) Refresh(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Inbox, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgInboxManage, true); err != nil {
		return Inbox{}, err
	}
	creds, err := s.credentials(ctx, orgID, id)
	if err != nil {
		return Inbox{}, err
	}
	numbers, err := s.meta.ListPhoneNumbers(ctx, creds.AccessToken, creds.WABAID)
	var ae *meta.APIError
	if errors.As(err, &ae) && ae.AuthFailed() {
		if merr := s.repo.MarkNeedsReauth(ctx, orgID, id, creds.AccountID); merr != nil {
			return Inbox{}, merr
		}
		return s.get(ctx, orgID, id)
	}
	if err != nil {
		return Inbox{}, fromMeta(err)
	}
	for _, n := range numbers {
		if n.ID == creds.PhoneNumberID {
			if err := s.repo.RefreshInfo(ctx, orgID, id, n.DisplayPhone, n.VerifiedName, n.QualityRating, n.MessagingTier); err != nil {
				return Inbox{}, err
			}
			return s.get(ctx, orgID, id)
		}
	}
	return Inbox{}, apperr.Conflict("el número ya no pertenece a la cuenta en Meta")
}

// ---------------------------------------------------------------------------
// Uso interno (sin usuario): el envío y el webhook
// ---------------------------------------------------------------------------

// Credentials descifra el token de una bandeja para hablar con Meta. Solo lo llaman el envío y el
// mantenimiento; nunca una ruta HTTP.
func (s *Service) Credentials(ctx context.Context, orgID, inboxID uuid.UUID) (Credentials, error) {
	c, err := s.credentials(ctx, orgID, inboxID)
	if err != nil {
		return Credentials{}, err
	}
	return c, nil
}

func (s *Service) credentials(ctx context.Context, orgID, inboxID uuid.UUID) (Credentials, error) {
	if err := s.requireBox(); err != nil {
		return Credentials{}, err
	}
	stored, err := s.repo.Stored(ctx, orgID, inboxID)
	if errors.Is(err, ErrNotFound) {
		return Credentials{}, apperr.NotFound("bandeja no encontrada")
	}
	if err != nil {
		return Credentials{}, err
	}
	tok, err := s.box.Open(stored.TokenEnc, tokenAAD(stored.AccountID))
	if err != nil {
		// No se descifra: llave equivocada o dato alterado. Es un problema del servidor.
		s.log.Error("no se pudo descifrar el token de una bandeja", "inbox_id", inboxID, "organization_id", orgID)
		return Credentials{}, err
	}
	return Credentials{
		InboxID: stored.InboxID, OrganizationID: stored.OrganizationID, AccountID: stored.AccountID,
		PhoneNumberID: stored.PhoneNumberID, WABAID: stored.WABAID, AccessToken: tok, ChannelStatus: stored.ChannelStatus,
	}, nil
}

// MarkNeedsReauth marca el token como inválido (Meta lo rechazó al enviar): la interfaz avisa que
// hay que cargar uno nuevo y no se siguen intentando envíos que van a fallar.
func (s *Service) MarkNeedsReauth(ctx context.Context, orgID, inboxID, accountID uuid.UUID) error {
	return s.repo.MarkNeedsReauth(ctx, orgID, inboxID, accountID)
}

// OpenAppSecret descifra el secreto de app propio de una cuenta ("" si no tiene).
func (s *Service) OpenAppSecret(accountID uuid.UUID, enc *string) (string, error) {
	if enc == nil || *enc == "" {
		return "", nil
	}
	if err := s.requireBox(); err != nil {
		return "", err
	}
	return s.box.Open(*enc, secretAAD(accountID))
}

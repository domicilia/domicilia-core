package organizations

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// InvitationTTL es la vigencia de una invitación.
const InvitationTTL = 7 * 24 * time.Hour

// tokenBytes son los bytes aleatorios de un token: 256 bits, imposible de adivinar.
const tokenBytes = 32

// Invitation es una invitación pendiente. El token en claro no se guarda ni se
// vuelve a mostrar: solo existe en el enlace que recibió quien se invitó.
type Invitation struct {
	ID             uuid.UUID  `json:"id"`
	OrganizationID uuid.UUID  `json:"organization_id"`
	Email          string     `json:"email"`
	Role           string     `json:"role"`
	RoleID         uuid.UUID  `json:"role_id"`
	RoleName       string     `json:"role_name"`
	InvitedBy      *uuid.UUID `json:"invited_by"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
}

// IssuedInvitation es una invitación recién emitida, con el enlace para aceptarla.
// Es la única vez que el enlace sale del servidor hacia quien invita; sirve para
// compartirlo (p. ej. por WhatsApp) mientras no haya proveedor de correo.
type IssuedInvitation struct {
	Invitation
	AcceptURL string `json:"accept_url"`
}

// InvitationPreview es lo que ve quien abre el enlace, antes de iniciar sesión.
type InvitationPreview struct {
	OrganizationName string    `json:"organization_name"`
	OrganizationSlug string    `json:"organization_slug"`
	Email            string    `json:"email"`
	RoleName         string    `json:"role_name"`
	ExpiresAt        time.Time `json:"expires_at"`
}

// newToken genera un token nuevo y el hash que se guarda. Solo el hash toca la base:
// quien lea la tabla no puede aceptar invitaciones.
func newToken() (token string, hash []byte, err error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("organizations: generar token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashToken(token), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

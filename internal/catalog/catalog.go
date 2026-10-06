// Package catalog es el menú de una organización: categorías, productos, sus variantes (tamaños)
// y los grupos de modificadores (extras, salsas...) que se les pueden aplicar. Diseño completo en
// docs/ecommerce.md — primera pieza del módulo de e-commerce (catalog → orders → payments →
// promotions).
//
// Dos reglas de negocio atraviesan todo el paquete:
//
//  1. El precio SIEMPRE vive en la variante, nunca en el producto. Un producto sin tamaños tiene
//     una única variante ("Regular") marcada IsDefault. Así hay un solo camino para calcular un
//     precio, nunca dos que puedan desincronizarse.
//  2. Una variante o una opción de modificador nunca se borra de verdad una vez creada: se
//     desactiva (IsActive = false). El día que exista un pedido, este referenciará una variante
//     por id — esa fila tiene que seguir existiendo aunque ya no esté a la venta.
package catalog

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/platform/apperr"
)

// Errores del repositorio que el servicio traduce a errores de negocio.
var (
	ErrNotFound         = errors.New("catalog: no encontrado")
	ErrDuplicateName    = errors.New("catalog: nombre repetido")
	ErrCategoryNotFound = errors.New("catalog: categoría no encontrada")
)

// MediaUploader es lo mínimo que el catálogo necesita para subir una foto de producto —
// internal/platform/azblob.Client lo satisface sin saberlo, mismo patrón angosto que
// orders.CatalogReader o payments.OrdersGateway. nil significa "sin almacenamiento de fotos
// configurado en este servidor": subir una responde 503, igual que WhatsApp sin
// CORE_SECRETS_KEY o los pagos sin CORE_EPAYCO_*.
type MediaUploader interface {
	Upload(ctx context.Context, blobName string, content io.Reader, size int64, contentType string) (publicURL string, err error)
}

// Límites de la foto de un producto. maxImageBytes es a propósito más chico que
// config.MaxBodyBytes (8 MiB): deja margen para el resto de la petición multipart y da un
// mensaje de negocio claro en vez de que el límite global del servidor corte la conexión.
const (
	maxImageBytes = 5 << 20 // 5 MiB
)

// allowedImageTypes son los tipos reales (detectados por contenido, nunca por el Content-Type
// que mande el cliente) que se aceptan como foto de producto.
var allowedImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// Tipos de selección de un grupo de modificadores.
const (
	SelectionSingle   = "single"   // radio: como mucho una opción
	SelectionMultiple = "multiple" // checkboxes: entre min y max opciones
)

// Límites de los campos. Los mismos números que ya validan las tablas (CHECK), repetidos aquí
// para devolver el error ANTES de tocar la base — un 422 claro en vez de un 500 de violación de
// restricción.
const (
	maxCategoryName      = 100
	maxProductName       = 150
	maxDescription       = 1000
	maxVariantName       = 100
	maxModifierGroupName = 100
	maxOptionName        = 100
	// maxPriceCents acota entradas absurdas (un error de tecleo con demasiados ceros), no es un
	// límite de negocio real.
	maxPriceCents       = 100_000_000
	maxVariants         = 30
	maxOptions          = 50
	maxGroupsPerProduct = 20
	maxIngredients      = 50
	maxIngredientLen    = 60
)

// Canales donde se puede publicar un producto. Ver el comentario de Product.Channels.
const (
	ChannelEcommerce = "ecommerce"
	ChannelWhatsApp  = "whatsapp"
)

var allowedChannels = map[string]struct{}{
	ChannelEcommerce: {},
	ChannelWhatsApp:  {},
}

// Category agrupa productos en el menú (p. ej. "Entradas", "Pizzas").
type Category struct {
	ID             uuid.UUID `json:"id"`
	OrganizationID uuid.UUID `json:"organization_id"`
	Name           string    `json:"name"`
	Position       int       `json:"position"`
	IsActive       bool      `json:"is_active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Variant es una versión comprable de un producto (un tamaño). El precio vive aquí.
type Variant struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	PriceCents int32     `json:"price_cents"`
	IsDefault  bool      `json:"is_default"`
	IsActive   bool      `json:"is_active"`
	Position   int       `json:"position"`
}

// Product es un artículo del menú, con sus variantes y los grupos de modificadores que aplican.
type Product struct {
	ID               uuid.UUID   `json:"id"`
	OrganizationID   uuid.UUID   `json:"organization_id"`
	CategoryID       *uuid.UUID  `json:"category_id"`
	Name             string      `json:"name"`
	Description      *string     `json:"description"`
	ImageURL         *string     `json:"image_url"`
	Position         int         `json:"position"`
	IsActive         bool        `json:"is_active"`
	Variants         []Variant   `json:"variants"`
	ModifierGroupIDs []uuid.UUID `json:"modifier_group_ids"`
	// Ingredients es texto libre, sin validar contra nada (ver docs/ecommerce.md §7) — etiquetas
	// que la organización escribe, no una tabla de ingredientes compartida.
	Ingredients []string `json:"ingredients"`
	// Channels es dónde se publica: 'ecommerce' lo hace visible en el feed público
	// (GET /v1/public/products), nunca is_active solo — una organización tiene que elegirlo a
	// propósito. 'whatsapp' queda reservado para cuando el agente de WhatsApp venda productos.
	Channels  []string  `json:"channels"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ModifierOption es una opción dentro de un grupo (p. ej. "Queso extra", "+$3.000").
type ModifierOption struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	PriceDeltaCents int32     `json:"price_delta_cents"`
	IsActive        bool      `json:"is_active"`
	Position        int       `json:"position"`
}

// ModifierGroup es un grupo reutilizable entre productos (p. ej. "Extras de pizza"). MinSelect >=
// 1 es lo que hace al grupo obligatorio — no hay un campo "required" aparte que pueda
// contradecirlo.
type ModifierGroup struct {
	ID             uuid.UUID        `json:"id"`
	OrganizationID uuid.UUID        `json:"organization_id"`
	Name           string           `json:"name"`
	SelectionType  string           `json:"selection_type"`
	MinSelect      int              `json:"min_select"`
	MaxSelect      int              `json:"max_select"`
	IsActive       bool             `json:"is_active"`
	Options        []ModifierOption `json:"options"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// VariantInput es una variante en una escritura de producto. ID nil crea una nueva; ID presente
// actualiza la existente (debe pertenecer al producto). Cualquier variante existente que NO
// aparezca en la lista se desactiva — nunca se borra (ver el comentario del paquete).
type VariantInput struct {
	ID         *uuid.UUID
	Name       string
	PriceCents int32
	IsDefault  bool
}

func (v VariantInput) validate() error {
	name := strings.TrimSpace(v.Name)
	if name == "" || len(name) > maxVariantName {
		return apperr.Invalid("el nombre de la variante debe tener entre 1 y " + strconv.Itoa(maxVariantName) + " caracteres")
	}
	if v.PriceCents < 0 || v.PriceCents > maxPriceCents {
		return apperr.Invalid("price_cents debe estar entre 0 y " + strconv.Itoa(maxPriceCents))
	}
	return nil
}

// validateVariants exige al menos una variante y EXACTAMENTE una marcada por defecto — si ninguna
// lo está, se marca la primera; si hay más de una, es un error del cliente (ambigüedad real, no
// algo que el servidor deba adivinar).
func validateVariants(in []VariantInput) ([]VariantInput, error) {
	if len(in) == 0 {
		return nil, apperr.Invalid("el producto necesita al menos una variante (el precio vive ahí)")
	}
	if len(in) > maxVariants {
		return nil, apperr.Invalid("un producto admite hasta " + strconv.Itoa(maxVariants) + " variantes")
	}
	defaults := 0
	for i := range in {
		if err := in[i].validate(); err != nil {
			return nil, err
		}
		if in[i].IsDefault {
			defaults++
		}
	}
	if defaults > 1 {
		return nil, apperr.Invalid("solo una variante puede ser la de por defecto")
	}
	if defaults == 0 {
		in[0].IsDefault = true
	}
	return in, nil
}

// OptionInput es una opción en una escritura de grupo de modificadores. Mismo patrón que
// VariantInput: ID nil crea, ID presente actualiza, ausente de la lista desactiva.
type OptionInput struct {
	ID              *uuid.UUID
	Name            string
	PriceDeltaCents int32
}

func (o OptionInput) validate() error {
	name := strings.TrimSpace(o.Name)
	if name == "" || len(name) > maxOptionName {
		return apperr.Invalid("el nombre de la opción debe tener entre 1 y " + strconv.Itoa(maxOptionName) + " caracteres")
	}
	if o.PriceDeltaCents < 0 || o.PriceDeltaCents > maxPriceCents {
		return apperr.Invalid("price_delta_cents debe estar entre 0 y " + strconv.Itoa(maxPriceCents))
	}
	return nil
}

func validateOptions(in []OptionInput) error {
	if len(in) == 0 {
		return apperr.Invalid("el grupo necesita al menos una opción")
	}
	if len(in) > maxOptions {
		return apperr.Invalid("un grupo admite hasta " + strconv.Itoa(maxOptions) + " opciones")
	}
	for i := range in {
		if err := in[i].validate(); err != nil {
			return err
		}
	}
	return nil
}

// validateModifierGroupIDs descarta duplicados (un id repetido chocaría con la clave primaria
// compuesta de product_modifier_groups al escribir) y acota la cantidad.
func validateModifierGroupIDs(ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) > maxGroupsPerProduct {
		return nil, apperr.Invalid("un producto admite hasta " + strconv.Itoa(maxGroupsPerProduct) + " grupos de modificadores")
	}
	seen := make(map[uuid.UUID]struct{}, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			return nil, apperr.Invalid("modifier_group_ids no admite un id vacío")
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// validateIngredients recorta espacios, descarta vacíos y acota cantidad/largo — texto libre de
// la organización, sin validar contra ninguna lista (ver el comentario de Product.Ingredients).
func validateIngredients(in []string) ([]string, error) {
	if len(in) > maxIngredients {
		return nil, apperr.Invalid("un producto admite hasta " + strconv.Itoa(maxIngredients) + " ingredientes")
	}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if len(v) > maxIngredientLen {
			return nil, apperr.Invalid("cada ingrediente admite hasta " + strconv.Itoa(maxIngredientLen) + " caracteres")
		}
		out = append(out, v)
	}
	return out, nil
}

// validateChannels exige valores conocidos (ecommerce, whatsapp) y descarta duplicados — nunca
// vacío por error de tipeo pasando desapercibido como "no publicado".
func validateChannels(in []string) ([]string, error) {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, c := range in {
		if _, ok := allowedChannels[c]; !ok {
			return nil, apperr.Invalid("channels solo admite \"ecommerce\" o \"whatsapp\"")
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out, nil
}

func validateSelection(selectionType string, minSelect, maxSelect int) error {
	if selectionType != SelectionSingle && selectionType != SelectionMultiple {
		return apperr.Invalid("selection_type debe ser single o multiple")
	}
	if minSelect < 0 || maxSelect < 1 || maxSelect < minSelect {
		return apperr.Invalid("min_select debe ser >= 0 y max_select >= 1 y >= min_select")
	}
	// Un grupo "single" es un radio button: como mucho una opción. Permitir max_select > 1 ahí
	// sería un grupo que dice una cosa (single) y se comporta como otra (multiple).
	if selectionType == SelectionSingle && maxSelect != 1 {
		return apperr.Invalid("un grupo single admite max_select = 1")
	}
	return nil
}

// validateName exige un nombre no vacío hasta max caracteres. No es "opcional" en el sentido de
// admitir ausencia — el llamador decide si lo invoca según si el campo vino en la petición.
func validateName(raw string, max int) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > max {
		return "", apperr.Invalid("el nombre debe tener entre 1 y " + strconv.Itoa(max) + " caracteres")
	}
	return name, nil
}

func optionalDescription(in *string) (*string, error) {
	if in == nil {
		return nil, nil //nolint:nilnil // ausente deja el campo en blanco
	}
	v := strings.TrimSpace(*in)
	if v == "" {
		return nil, nil //nolint:nilnil // vacío también lo deja en blanco
	}
	if len(v) > maxDescription {
		return nil, apperr.Invalid("description admite hasta " + strconv.Itoa(maxDescription) + " caracteres")
	}
	return &v, nil
}

// Repository es lo que el servicio necesita de la base de datos.
type Repository interface {
	InsertCategory(ctx context.Context, orgID uuid.UUID, name string, position int) (Category, error)
	GetCategory(ctx context.Context, orgID, id uuid.UUID) (Category, error)
	ListCategories(ctx context.Context, orgID uuid.UUID) ([]Category, error)
	UpdateCategory(ctx context.Context, orgID, id uuid.UUID, p CategoryPatch) (Category, error)

	InsertProduct(ctx context.Context, n NewProduct) (Product, error)
	GetProduct(ctx context.Context, orgID, id uuid.UUID) (Product, error)
	ListProducts(ctx context.Context, orgID uuid.UUID, categoryID *uuid.UUID, limit, offset int) ([]Product, int64, error)
	UpdateProduct(ctx context.Context, orgID, id uuid.UUID, p ProductPatch) (Product, error)

	InsertModifierGroup(ctx context.Context, n NewModifierGroup) (ModifierGroup, error)
	GetModifierGroup(ctx context.Context, orgID, id uuid.UUID) (ModifierGroup, error)
	ListModifierGroups(ctx context.Context, orgID uuid.UUID) ([]ModifierGroup, error)
	// GetModifierGroupsByIDs lo usa internal/orders para armar la línea de un pedido: ver
	// db/queries/catalog.sql. Ids desconocidos o de otra organización se omiten sin error —
	// quien llama decide si eso es un problema (p. ej. si faltó alguno de los pedidos).
	GetModifierGroupsByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]ModifierGroup, error)
	UpdateModifierGroup(ctx context.Context, orgID, id uuid.UUID, p ModifierGroupPatch) (ModifierGroup, error)

	// ListPublicProducts es el feed sin sesión de Inicio (§7 de docs/ecommerce.md): productos de
	// varias organizaciones ACTIVAS a la vez, cada uno con al menos una variante activa (sin eso
	// no hay nada que vender). filter.OrganizationID acota a UN restaurante, para su propia
	// vitrina — el resto de filtros son opcionales.
	ListPublicProducts(ctx context.Context, filter PublicFeedFilter, limit, offset int) ([]FeedProduct, int64, error)
}

// NewProduct son los datos para crear un producto.
type NewProduct struct {
	OrganizationID   uuid.UUID
	CategoryID       *uuid.UUID
	Name             string
	Description      *string
	ImageURL         *string
	Position         int
	Variants         []VariantInput
	ModifierGroupIDs []uuid.UUID
	// Ingredients y Channels nil se guardan como "sin nada" (arreglo vacío) — un producto nuevo
	// nunca se publica solo, ver el comentario de Product.Channels.
	Ingredients []string
	Channels    []string
}

// ProductPatch es un cambio parcial: un puntero/slice nil no toca el campo. En CategoryID,
// Description e ImageURL, SetX distingue "no tocar" de "dejar en blanco" (sí pueden ser null).
type ProductPatch struct {
	SetCategory bool
	CategoryID  *uuid.UUID
	Name        *string
	SetDesc     bool
	Description *string
	SetImage    bool
	ImageURL    *string
	Position    *int
	IsActive    *bool
	// Variants/ModifierGroupIDs nil: no se tocan. No nil (incluso vacío): reemplazan la lista
	// completa (upsert + desactivar lo que ya no viene).
	Variants         *[]VariantInput
	ModifierGroupIDs *[]uuid.UUID
	// Ingredients/Channels: mismo patrón que Variants — nil no toca, no nil (incluso []) reemplaza
	// completo. Channels es el interruptor real de "publicar": pasar [] lo despublica de todos
	// los canales sin desactivar el producto.
	Ingredients *[]string
	Channels    *[]string
}

// CategoryPatch es un cambio parcial de categoría. Ningún campo admite null: siempre puntero =
// "no tocar".
type CategoryPatch struct {
	Name     *string
	Position *int
	IsActive *bool
}

// FeedProduct es un producto tal como aparece en el feed público (Inicio del cliente): liviano a
// propósito — ni variantes ni modificadores, solo lo que entra en una tarjeta. Para configurar y
// agregar al carrito, el cliente pide el detalle completo (ver ProductDetail).
type FeedProduct struct {
	ID                  uuid.UUID  `json:"id"`
	OrganizationID      uuid.UUID  `json:"organization_id"`
	OrganizationName    string     `json:"organization_name"`
	OrganizationSlug    string     `json:"organization_slug"`
	OrganizationLogoURL *string    `json:"organization_logo_url"`
	CategoryID          *uuid.UUID `json:"category_id"`
	CategoryName        *string    `json:"category_name"`
	Name                string     `json:"name"`
	Description         *string    `json:"description"`
	ImageURL            *string    `json:"image_url"`
	// MinPriceCents es el precio de la variante activa más barata — "desde $X" en la tarjeta.
	MinPriceCents int32 `json:"min_price_cents"`
}

// PublicFeedFilter filtra el feed público. Todo opcional: sin nada, trae de todas las
// organizaciones activas.
type PublicFeedFilter struct {
	OrganizationID *uuid.UUID
	// OrganizationSlug es para la vitrina de un restaurante: esa página solo conoce el slug de
	// la URL, nunca el id — evita que el frontend tenga que resolverlo antes de poder pedir el
	// menú. Si ambos vienen, los dos se aplican (en la práctica solo se usa uno a la vez).
	OrganizationSlug *string
	Search           *string
	// Category filtra por el NOMBRE de categoría (sin distinguir mayúsculas) — las categorías son
	// por organización, no una taxonomía compartida; esto es una coincidencia de texto a
	// propósito, no un id.
	Category *string
}

// ProductDetail es un producto con TODO lo necesario para configurarlo y agregarlo al carrito
// (variantes, grupos de modificadores con sus opciones) — lo que el feed público no trae.
type ProductDetail struct {
	Product
	ModifierGroups []ModifierGroup `json:"modifier_groups"`
}

// NewModifierGroup son los datos para crear un grupo de modificadores.
type NewModifierGroup struct {
	OrganizationID uuid.UUID
	Name           string
	SelectionType  string
	MinSelect      int
	MaxSelect      int
	Options        []OptionInput
}

// ModifierGroupPatch es un cambio parcial de grupo. Options nil: no se toca. No nil: reemplaza
// completo (mismo patrón que Variants en ProductPatch).
type ModifierGroupPatch struct {
	Name          *string
	SelectionType *string
	MinSelect     *int
	MaxSelect     *int
	IsActive      *bool
	Options       *[]OptionInput
}

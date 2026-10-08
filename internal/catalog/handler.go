package catalog

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
)

// Handler expone el catálogo por HTTP.
type Handler struct{ svc *Service }

// NewHandler crea el handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// RegisterPublic registra las rutas sin autenticación: el feed de Inicio y el detalle de un
// producto para configurarlo (variantes, modificadores) — docs/ecommerce.md §7. Quien navega no
// necesariamente tiene cuenta todavía.
func (h *Handler) RegisterPublic(g *echo.Group) {
	g.GET("/public/products", h.publicFeed)
	g.GET("/public/organizations/:org_id/products/:product_id", h.publicProductDetail)
}

// Register registra las rutas. g ya exige un usuario de negocio activo.
func (h *Handler) Register(g *echo.Group) {
	g.GET("/organizations/:org_id/categories", h.listCategories)
	g.POST("/organizations/:org_id/categories", h.createCategory)
	g.PATCH("/organizations/:org_id/categories/:category_id", h.updateCategory)

	g.GET("/organizations/:org_id/products", h.listProducts)
	g.POST("/organizations/:org_id/products", h.createProduct)
	g.GET("/organizations/:org_id/products/:product_id", h.getProduct)
	g.PATCH("/organizations/:org_id/products/:product_id", h.updateProduct)
	g.POST("/organizations/:org_id/products/:product_id/image", h.uploadProductImage)

	g.GET("/organizations/:org_id/modifier-groups", h.listModifierGroups)
	g.POST("/organizations/:org_id/modifier-groups", h.createModifierGroup)
	g.GET("/organizations/:org_id/modifier-groups/:modifier_group_id", h.getModifierGroup)
	g.PATCH("/organizations/:org_id/modifier-groups/:modifier_group_id", h.updateModifierGroup)
}

func actorOrg(c *echo.Context) (identity.Principal, uuid.UUID, error) {
	p, err := identity.Current(c)
	if err != nil {
		return identity.Principal{}, uuid.Nil, err
	}
	org, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return identity.Principal{}, uuid.Nil, err
	}
	return p, org, nil
}

func optionalString(c *echo.Context, name string) *string {
	v := c.QueryParam(name)
	if v == "" {
		return nil
	}
	return &v
}

func optionalUUID(c *echo.Context, name string) (*uuid.UUID, error) {
	s := c.QueryParam(name)
	if s == "" {
		return nil, nil //nolint:nilnil // ausente es un valor válido
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, apperr.Invalid(name + " no es un identificador válido")
	}
	return &id, nil
}

// ---------------------------------------------------------------------------
// Categorías
// ---------------------------------------------------------------------------

type createCategoryRequest struct {
	Name     string `json:"name"`
	Position int    `json:"position"`
}

type updateCategoryRequest struct {
	Name     *string `json:"name"`
	Position *int    `json:"position"`
	IsActive *bool   `json:"is_active"`
}

func (h *Handler) listCategories(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	rows, err := h.svc.ListCategories(c.Request().Context(), p, org)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, rows)
}

func (h *Handler) createCategory(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	var in createCategoryRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.CreateCategory(c.Request().Context(), p, org, CreateCategoryInput(in))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, out)
}

func (h *Handler) updateCategory(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "category_id")
	if err != nil {
		return err
	}
	var in updateCategoryRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.UpdateCategory(c.Request().Context(), p, org, id, UpdateCategoryInput(in))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Productos
// ---------------------------------------------------------------------------

type variantRequest struct {
	ID         *uuid.UUID `json:"id"`
	Name       string     `json:"name"`
	PriceCents int32      `json:"price_cents"`
	IsDefault  bool       `json:"is_default"`
}

func toVariantInputs(in []variantRequest) []VariantInput {
	out := make([]VariantInput, len(in))
	for i, v := range in {
		out[i] = VariantInput(v)
	}
	return out
}

type createProductRequest struct {
	CategoryID       *uuid.UUID       `json:"category_id"`
	Name             string           `json:"name"`
	Description      *string          `json:"description"`
	ImageURL         *string          `json:"image_url"`
	Position         int              `json:"position"`
	Variants         []variantRequest `json:"variants"`
	ModifierGroupIDs []uuid.UUID      `json:"modifier_group_ids"`
	Ingredients      []string         `json:"ingredients"`
	Channels         []string         `json:"channels"`
	PromoDiscountBps int32            `json:"promo_discount_bps"`
}

type updateProductRequest struct {
	CategoryID       httpserver.Field[uuid.UUID] `json:"category_id"`
	Name             *string                     `json:"name"`
	Description      httpserver.Field[string]    `json:"description"`
	ImageURL         httpserver.Field[string]    `json:"image_url"`
	Position         *int                        `json:"position"`
	IsActive         *bool                       `json:"is_active"`
	Variants         *[]variantRequest           `json:"variants"`
	ModifierGroupIDs *[]uuid.UUID                `json:"modifier_group_ids"`
	Ingredients      *[]string                   `json:"ingredients"`
	Channels         *[]string                   `json:"channels"`
	PromoDiscountBps *int32                      `json:"promo_discount_bps"`
}

func (h *Handler) listProducts(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	category, err := optionalUUID(c, "category_id")
	if err != nil {
		return err
	}
	page, err := httpserver.PageParams(c)
	if err != nil {
		return err
	}
	rows, total, err := h.svc.ListProducts(c.Request().Context(), p, org, ListProductsInput{
		CategoryID: category, Limit: page.Limit, Offset: page.Offset,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpserver.NewPaged(rows, total, page))
}

func (h *Handler) createProduct(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	var in createProductRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.CreateProduct(c.Request().Context(), p, org, CreateProductInput{
		CategoryID: in.CategoryID, Name: in.Name, Description: in.Description, ImageURL: in.ImageURL,
		Position: in.Position, Variants: toVariantInputs(in.Variants), ModifierGroupIDs: in.ModifierGroupIDs,
		Ingredients: in.Ingredients, Channels: in.Channels, PromoDiscountBps: in.PromoDiscountBps,
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, out)
}

func (h *Handler) getProduct(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "product_id")
	if err != nil {
		return err
	}
	out, err := h.svc.GetProduct(c.Request().Context(), p, org, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) updateProduct(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "product_id")
	if err != nil {
		return err
	}
	var in updateProductRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	upd := UpdateProductInput{
		CategoryID: in.CategoryID, Name: in.Name, Description: in.Description, ImageURL: in.ImageURL,
		Position: in.Position, IsActive: in.IsActive, ModifierGroupIDs: in.ModifierGroupIDs,
		Ingredients: in.Ingredients, Channels: in.Channels, PromoDiscountBps: in.PromoDiscountBps,
	}
	if in.Variants != nil {
		v := toVariantInputs(*in.Variants)
		upd.Variants = &v
	}
	out, err := h.svc.UpdateProduct(c.Request().Context(), p, org, id, upd)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) uploadProductImage(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "product_id")
	if err != nil {
		return err
	}
	file, err := c.FormFile("image")
	if err != nil {
		return apperr.Invalid("falta el archivo \"image\" en el formulario")
	}
	src, err := file.Open()
	if err != nil {
		return apperr.Invalid("no se pudo leer el archivo")
	}
	defer func() { _ = src.Close() }()

	out, err := h.svc.UploadProductImage(c.Request().Context(), p, org, id, src, file.Size)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Grupos de modificadores
// ---------------------------------------------------------------------------

type optionRequest struct {
	ID              *uuid.UUID `json:"id"`
	Name            string     `json:"name"`
	PriceDeltaCents int32      `json:"price_delta_cents"`
}

func toOptionInputs(in []optionRequest) []OptionInput {
	out := make([]OptionInput, len(in))
	for i, o := range in {
		out[i] = OptionInput(o)
	}
	return out
}

type createModifierGroupRequest struct {
	Name          string          `json:"name"`
	SelectionType string          `json:"selection_type"`
	MinSelect     int             `json:"min_select"`
	MaxSelect     int             `json:"max_select"`
	Options       []optionRequest `json:"options"`
}

type updateModifierGroupRequest struct {
	Name          *string          `json:"name"`
	SelectionType *string          `json:"selection_type"`
	MinSelect     *int             `json:"min_select"`
	MaxSelect     *int             `json:"max_select"`
	IsActive      *bool            `json:"is_active"`
	Options       *[]optionRequest `json:"options"`
}

func (h *Handler) listModifierGroups(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	rows, err := h.svc.ListModifierGroups(c.Request().Context(), p, org)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, rows)
}

func (h *Handler) createModifierGroup(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	var in createModifierGroupRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	out, err := h.svc.CreateModifierGroup(c.Request().Context(), p, org, CreateModifierGroupInput{
		Name: in.Name, SelectionType: in.SelectionType, MinSelect: in.MinSelect, MaxSelect: in.MaxSelect,
		Options: toOptionInputs(in.Options),
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, out)
}

func (h *Handler) getModifierGroup(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "modifier_group_id")
	if err != nil {
		return err
	}
	out, err := h.svc.GetModifierGroup(c.Request().Context(), p, org, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) updateModifierGroup(c *echo.Context) error {
	p, org, err := actorOrg(c)
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "modifier_group_id")
	if err != nil {
		return err
	}
	var in updateModifierGroupRequest
	if err := httpserver.Bind(c, &in); err != nil {
		return err
	}
	upd := UpdateModifierGroupInput{
		Name: in.Name, SelectionType: in.SelectionType, MinSelect: in.MinSelect,
		MaxSelect: in.MaxSelect, IsActive: in.IsActive,
	}
	if in.Options != nil {
		o := toOptionInputs(*in.Options)
		upd.Options = &o
	}
	out, err := h.svc.UpdateModifierGroup(c.Request().Context(), p, org, id, upd)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Feed público
// ---------------------------------------------------------------------------

func (h *Handler) publicFeed(c *echo.Context) error {
	page, err := httpserver.PageParams(c)
	if err != nil {
		return err
	}
	orgID, err := optionalUUID(c, "organization_id")
	if err != nil {
		return err
	}
	filter := PublicFeedFilter{
		OrganizationID:   orgID,
		OrganizationSlug: optionalString(c, "organization_slug"),
		Search:           optionalString(c, "q"),
		Category:         optionalString(c, "category"),
	}
	rows, total, err := h.svc.PublicFeed(c.Request().Context(), filter, page.Limit, page.Offset)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, httpserver.NewPaged(rows, total, page))
}

func (h *Handler) publicProductDetail(c *echo.Context) error {
	org, err := httpserver.UUIDParam(c, "org_id")
	if err != nil {
		return err
	}
	id, err := httpserver.UUIDParam(c, "product_id")
	if err != nil {
		return err
	}
	out, err := h.svc.PublicGetProductDetail(c.Request().Context(), org, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/domicilia/domicilia-core/internal/access"
	"github.com/domicilia/domicilia-core/internal/identity"
	"github.com/domicilia/domicilia-core/internal/platform/apperr"
	"github.com/domicilia/domicilia-core/internal/platform/httpserver"
	"github.com/domicilia/domicilia-core/internal/pricing"
	"github.com/domicilia/domicilia-core/internal/tenant"
)

// Pricer resuelve las tarifas vigentes de una organización (internal/pricing). Las lecturas
// públicas del catálogo devuelven el precio PUBLICADO (local + comisión), nunca el local.
type Pricer interface {
	RatesFor(ctx context.Context, orgID uuid.UUID) (pricing.Rates, error)
}

// Service reúne las reglas del catálogo.
type Service struct {
	repo   Repository
	gate   *tenant.Gate
	media  MediaUploader
	pricer Pricer
}

// NewService crea el servicio. media nil es válido: significa "sin almacenamiento de fotos
// configurado" — ver el comentario de MediaUploader.
func NewService(repo Repository, gate *tenant.Gate, media MediaUploader, pricer Pricer) *Service {
	return &Service{repo: repo, gate: gate, media: media, pricer: pricer}
}

// ---------------------------------------------------------------------------
// Categorías
// ---------------------------------------------------------------------------

// CreateCategoryInput son los datos para crear una categoría.
type CreateCategoryInput struct {
	Name     string
	Position int
}

// CreateCategory crea una categoría. El nombre es único por organización (entre las activas).
func (s *Service) CreateCategory(ctx context.Context, actor identity.Principal, orgID uuid.UUID, in CreateCategoryInput) (Category, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogManage, true); err != nil {
		return Category{}, err
	}
	name, err := validateName(in.Name, maxCategoryName)
	if err != nil {
		return Category{}, err
	}
	c, err := s.repo.InsertCategory(ctx, orgID, name, in.Position)
	if errors.Is(err, ErrDuplicateName) {
		return Category{}, apperr.Conflict("ya existe una categoría activa con ese nombre")
	}
	return c, err
}

// ListCategories devuelve todas las categorías de la organización (activas e inactivas: quien
// administra el menú necesita ver ambas para poder reactivar una).
func (s *Service) ListCategories(ctx context.Context, actor identity.Principal, orgID uuid.UUID) ([]Category, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogRead, false); err != nil {
		return nil, err
	}
	return s.repo.ListCategories(ctx, orgID)
}

// UpdateCategoryInput es un cambio parcial: nil no toca el campo.
type UpdateCategoryInput struct {
	Name     *string
	Position *int
	IsActive *bool
}

// UpdateCategory cambia una categoría.
func (s *Service) UpdateCategory(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID, in UpdateCategoryInput) (Category, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogManage, true); err != nil {
		return Category{}, err
	}
	if in.Name == nil && in.Position == nil && in.IsActive == nil {
		return Category{}, apperr.Invalid("envía name, position o is_active")
	}
	p := CategoryPatch{Position: in.Position, IsActive: in.IsActive}
	if in.Name != nil {
		name, err := validateName(*in.Name, maxCategoryName)
		if err != nil {
			return Category{}, err
		}
		p.Name = &name
	}
	c, err := s.repo.UpdateCategory(ctx, orgID, id, p)
	switch {
	case errors.Is(err, ErrNotFound):
		return Category{}, apperr.NotFound("categoría no encontrada")
	case errors.Is(err, ErrDuplicateName):
		return Category{}, apperr.Conflict("ya existe una categoría activa con ese nombre")
	}
	return c, err
}

// ---------------------------------------------------------------------------
// Productos
// ---------------------------------------------------------------------------

// CreateProductInput son los datos para crear un producto.
type CreateProductInput struct {
	CategoryID       *uuid.UUID
	Name             string
	Description      *string
	ImageURL         *string
	Position         int
	Variants         []VariantInput
	ModifierGroupIDs []uuid.UUID
	Ingredients      []string
	Channels         []string
	PromoDiscountBps int32
}

// CreateProduct crea un producto con sus variantes (al menos una: ahí vive el precio) y, si
// vienen, los grupos de modificadores que aplican. Channels vacío (el valor por omisión) es a
// propósito: un producto nuevo no se publica solo, ver el comentario de Product.Channels.
func (s *Service) CreateProduct(ctx context.Context, actor identity.Principal, orgID uuid.UUID, in CreateProductInput) (Product, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogManage, true); err != nil {
		return Product{}, err
	}
	name, err := validateName(in.Name, maxProductName)
	if err != nil {
		return Product{}, err
	}
	desc, err := optionalDescription(in.Description)
	if err != nil {
		return Product{}, err
	}
	variants, err := validateVariants(in.Variants)
	if err != nil {
		return Product{}, err
	}
	groupIDs, err := validateModifierGroupIDs(in.ModifierGroupIDs)
	if err != nil {
		return Product{}, err
	}
	ingredients, err := validateIngredients(in.Ingredients)
	if err != nil {
		return Product{}, err
	}
	channels, err := validateChannels(in.Channels)
	if err != nil {
		return Product{}, err
	}
	if err := validatePromoDiscount(in.PromoDiscountBps); err != nil {
		return Product{}, err
	}
	p, err := s.repo.InsertProduct(ctx, NewProduct{
		OrganizationID: orgID, CategoryID: in.CategoryID, Name: name, Description: desc,
		ImageURL: in.ImageURL, Position: in.Position, Variants: variants, ModifierGroupIDs: groupIDs,
		Ingredients: ingredients, Channels: channels, PromoDiscountBps: in.PromoDiscountBps,
	})
	if errors.Is(err, ErrCategoryNotFound) {
		return Product{}, apperr.Invalid("category_id no existe")
	}
	return p, err
}

// GetProduct devuelve un producto de la organización, con sus variantes y grupos de modificadores.
func (s *Service) GetProduct(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (Product, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogRead, false); err != nil {
		return Product{}, err
	}
	p, err := s.repo.GetProduct(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) {
		return Product{}, apperr.NotFound("producto no encontrado")
	}
	return p, err
}

// ListProductsInput son los filtros del listado.
type ListProductsInput struct {
	CategoryID *uuid.UUID
	Limit      int
	Offset     int
}

// ListProducts devuelve una página de productos, opcionalmente filtrada por categoría.
func (s *Service) ListProducts(ctx context.Context, actor identity.Principal, orgID uuid.UUID, in ListProductsInput) ([]Product, int64, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogRead, false); err != nil {
		return nil, 0, err
	}
	return s.repo.ListProducts(ctx, orgID, in.CategoryID, in.Limit, in.Offset)
}

// UpdateProductInput es un cambio parcial. En CategoryID, Description e ImageURL, Field
// distingue ausente (no tocar) de null (dejar en blanco) — ver httpserver.Field. Variants y
// ModifierGroupIDs en nil no se tocan; no nil (incluso vacío) reemplaza la lista completa.
type UpdateProductInput struct {
	CategoryID       httpserver.Field[uuid.UUID]
	Name             *string
	Description      httpserver.Field[string]
	ImageURL         httpserver.Field[string]
	Position         *int
	IsActive         *bool
	Variants         *[]VariantInput
	ModifierGroupIDs *[]uuid.UUID
	// Ingredients/Channels: nil no toca, no nil (incluso []) reemplaza completo. Channels es el
	// interruptor real de publicar/despublicar — ver el comentario de ProductPatch.
	Ingredients *[]string
	Channels    *[]string
	// PromoDiscountBps nil no toca; 0 quita la promoción.
	PromoDiscountBps *int32
}

// UpdateProduct cambia un producto.
func (s *Service) UpdateProduct(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID, in UpdateProductInput) (Product, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogManage, true); err != nil {
		return Product{}, err
	}
	if !in.CategoryID.Set && in.Name == nil && !in.Description.Set && !in.ImageURL.Set &&
		in.Position == nil && in.IsActive == nil && in.Variants == nil && in.ModifierGroupIDs == nil &&
		in.Ingredients == nil && in.Channels == nil && in.PromoDiscountBps == nil {
		return Product{}, apperr.Invalid("envía al menos un campo para actualizar")
	}
	p := ProductPatch{
		SetCategory: in.CategoryID.Set, CategoryID: in.CategoryID.Value,
		SetImage: in.ImageURL.Set, ImageURL: in.ImageURL.Value,
		Position: in.Position, IsActive: in.IsActive,
	}
	if in.Name != nil {
		name, err := validateName(*in.Name, maxProductName)
		if err != nil {
			return Product{}, err
		}
		p.Name = &name
	}
	if in.Description.Set {
		desc, err := optionalDescription(in.Description.Value)
		if err != nil {
			return Product{}, err
		}
		p.SetDesc, p.Description = true, desc
	}
	if in.Variants != nil {
		variants, err := validateVariants(*in.Variants)
		if err != nil {
			return Product{}, err
		}
		p.Variants = &variants
	}
	if in.ModifierGroupIDs != nil {
		groupIDs, err := validateModifierGroupIDs(*in.ModifierGroupIDs)
		if err != nil {
			return Product{}, err
		}
		p.ModifierGroupIDs = &groupIDs
	}
	if in.Ingredients != nil {
		ingredients, err := validateIngredients(*in.Ingredients)
		if err != nil {
			return Product{}, err
		}
		p.Ingredients = &ingredients
	}
	if in.Channels != nil {
		channels, err := validateChannels(*in.Channels)
		if err != nil {
			return Product{}, err
		}
		p.Channels = &channels
	}
	if in.PromoDiscountBps != nil {
		if err := validatePromoDiscount(*in.PromoDiscountBps); err != nil {
			return Product{}, err
		}
		p.PromoDiscountBps = in.PromoDiscountBps
	}
	out, err := s.repo.UpdateProduct(ctx, orgID, id, p)
	switch {
	case errors.Is(err, ErrNotFound):
		return Product{}, apperr.NotFound("producto no encontrado")
	case errors.Is(err, ErrCategoryNotFound):
		return Product{}, apperr.Invalid("category_id no existe")
	}
	return out, err
}

// UploadProductImage sube la foto de un producto y guarda su URL pública — mismo permiso que
// cualquier otro cambio al producto (org.catalog.manage). El tipo real de archivo se detecta por
// contenido (los primeros 512 bytes), nunca por el Content-Type que mande el cliente: así no se
// puede subir, por ejemplo, un HTML disfrazado de imagen que luego el navegador de alguien más
// interprete al abrir la URL pública.
func (s *Service) UploadProductImage(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID, content io.Reader, size int64) (Product, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogManage, true); err != nil {
		return Product{}, err
	}
	if s.media == nil {
		return Product{}, apperr.Unavailable("la subida de fotos todavía no está configurada en este servidor")
	}
	// Se confirma que el producto existe ANTES de subir nada: una ruta/id equivocados no deben
	// dejar un blob huérfano en el storage.
	if _, err := s.repo.GetProduct(ctx, orgID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Product{}, apperr.NotFound("producto no encontrado")
		}
		return Product{}, err
	}
	if size <= 0 {
		return Product{}, apperr.Invalid("la foto está vacía")
	}
	if size > maxImageBytes {
		return Product{}, apperr.Invalid("la foto no puede superar 5 MB")
	}

	sniff := make([]byte, 512)
	n, err := io.ReadFull(content, sniff)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Product{}, fmt.Errorf("catalog: leer la foto: %w", err)
	}
	sniff = sniff[:n]
	contentType := http.DetectContentType(sniff)
	ext, ok := allowedImageTypes[contentType]
	if !ok {
		return Product{}, apperr.Invalid("la foto debe ser JPEG, PNG, WEBP o GIF")
	}

	blobName := fmt.Sprintf("%s/%s%s", orgID, uuid.NewString(), ext)
	url, err := s.media.Upload(ctx, blobName, io.MultiReader(bytes.NewReader(sniff), content), size, contentType)
	if err != nil {
		return Product{}, fmt.Errorf("catalog: subir la foto: %w", err)
	}

	out, err := s.repo.UpdateProduct(ctx, orgID, id, ProductPatch{SetImage: true, ImageURL: &url})
	if errors.Is(err, ErrNotFound) {
		return Product{}, apperr.NotFound("producto no encontrado")
	}
	return out, err
}

// ---------------------------------------------------------------------------
// Grupos de modificadores
// ---------------------------------------------------------------------------

// CreateModifierGroupInput son los datos para crear un grupo.
type CreateModifierGroupInput struct {
	Name          string
	SelectionType string
	MinSelect     int
	MaxSelect     int
	Options       []OptionInput
}

// CreateModifierGroup crea un grupo de modificadores con sus opciones (al menos una).
func (s *Service) CreateModifierGroup(ctx context.Context, actor identity.Principal, orgID uuid.UUID, in CreateModifierGroupInput) (ModifierGroup, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogManage, true); err != nil {
		return ModifierGroup{}, err
	}
	name, err := validateName(in.Name, maxModifierGroupName)
	if err != nil {
		return ModifierGroup{}, err
	}
	if err := validateSelection(in.SelectionType, in.MinSelect, in.MaxSelect); err != nil {
		return ModifierGroup{}, err
	}
	if err := validateOptions(in.Options); err != nil {
		return ModifierGroup{}, err
	}
	return s.repo.InsertModifierGroup(ctx, NewModifierGroup{
		OrganizationID: orgID, Name: name, SelectionType: in.SelectionType,
		MinSelect: in.MinSelect, MaxSelect: in.MaxSelect, Options: in.Options,
	})
}

// GetModifierGroup devuelve un grupo de modificadores con sus opciones.
func (s *Service) GetModifierGroup(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID) (ModifierGroup, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogRead, false); err != nil {
		return ModifierGroup{}, err
	}
	g, err := s.repo.GetModifierGroup(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) {
		return ModifierGroup{}, apperr.NotFound("grupo de modificadores no encontrado")
	}
	return g, err
}

// ListModifierGroups devuelve todos los grupos de la organización (no crecen sin control como un
// listado de pedidos: no hace falta paginar).
func (s *Service) ListModifierGroups(ctx context.Context, actor identity.Principal, orgID uuid.UUID) ([]ModifierGroup, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogRead, false); err != nil {
		return nil, err
	}
	return s.repo.ListModifierGroups(ctx, orgID)
}

// UpdateModifierGroupInput es un cambio parcial. Options en nil no se toca; no nil (incluso
// vacío) reemplaza la lista completa.
type UpdateModifierGroupInput struct {
	Name          *string
	SelectionType *string
	MinSelect     *int
	MaxSelect     *int
	IsActive      *bool
	Options       *[]OptionInput
}

// UpdateModifierGroup cambia un grupo de modificadores.
func (s *Service) UpdateModifierGroup(ctx context.Context, actor identity.Principal, orgID, id uuid.UUID, in UpdateModifierGroupInput) (ModifierGroup, error) {
	if _, err := s.gate.Open(ctx, actor, orgID, access.OrgCatalogManage, true); err != nil {
		return ModifierGroup{}, err
	}
	if in.Name == nil && in.SelectionType == nil && in.MinSelect == nil && in.MaxSelect == nil &&
		in.IsActive == nil && in.Options == nil {
		return ModifierGroup{}, apperr.Invalid("envía al menos un campo para actualizar")
	}
	p := ModifierGroupPatch{IsActive: in.IsActive}
	if in.Name != nil {
		name, err := validateName(*in.Name, maxModifierGroupName)
		if err != nil {
			return ModifierGroup{}, err
		}
		p.Name = &name
	}
	// selection_type, min_select y max_select se validan juntos: cambiar solo uno de los tres
	// podría dejar la combinación inconsistente (p. ej. min_select nuevo > max_select viejo).
	if in.SelectionType != nil || in.MinSelect != nil || in.MaxSelect != nil {
		current, err := s.repo.GetModifierGroup(ctx, orgID, id)
		if errors.Is(err, ErrNotFound) {
			return ModifierGroup{}, apperr.NotFound("grupo de modificadores no encontrado")
		}
		if err != nil {
			return ModifierGroup{}, err
		}
		selType, minSel, maxSel := current.SelectionType, current.MinSelect, current.MaxSelect
		if in.SelectionType != nil {
			selType = *in.SelectionType
		}
		if in.MinSelect != nil {
			minSel = *in.MinSelect
		}
		if in.MaxSelect != nil {
			maxSel = *in.MaxSelect
		}
		if err := validateSelection(selType, minSel, maxSel); err != nil {
			return ModifierGroup{}, err
		}
		p.SelectionType, p.MinSelect, p.MaxSelect = &selType, &minSel, &maxSel
	}
	if in.Options != nil {
		if err := validateOptions(*in.Options); err != nil {
			return ModifierGroup{}, err
		}
		p.Options = in.Options
	}
	out, err := s.repo.UpdateModifierGroup(ctx, orgID, id, p)
	if errors.Is(err, ErrNotFound) {
		return ModifierGroup{}, apperr.NotFound("grupo de modificadores no encontrado")
	}
	return out, err
}

// ---------------------------------------------------------------------------
// Feed público — Inicio del cliente (docs/ecommerce.md §7). Sin sesión, igual que
// organizations.Service.PublicList: quien navega el feed no necesariamente tiene cuenta todavía.
// ---------------------------------------------------------------------------

// PublicFeed lista productos de organizaciones activas, con al menos una variante activa cada
// uno. Sin filtro.OrganizationID, cruza todas las organizaciones a propósito (el feed mezclado).
func (s *Service) PublicFeed(ctx context.Context, filter PublicFeedFilter, limit, offset int) ([]FeedProduct, int64, error) {
	items, total, err := s.repo.ListPublicProducts(ctx, filter, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	// El feed cruza organizaciones: tarifas por organización, resueltas una sola vez cada una.
	rates := make(map[uuid.UUID]pricing.Rates)
	for i := range items {
		r, ok := rates[items[i].OrganizationID]
		if !ok {
			if r, err = s.pricer.RatesFor(ctx, items[i].OrganizationID); err != nil {
				return nil, 0, err
			}
			rates[items[i].OrganizationID] = r
		}
		local := int64(items[i].MinPriceCents)
		items[i].MinPriceCents = int32(r.PriceUnit(local, nil, items[i].PromoDiscountBps).TotalCents) //nolint:gosec // acotado por maxPriceCents
		if items[i].PromoDiscountBps > 0 {
			items[i].RegularMinPriceCents = int32(r.PriceUnit(local, nil, 0).TotalCents) //nolint:gosec
		} else {
			items[i].RegularMinPriceCents = items[i].MinPriceCents
		}
	}
	return items, total, nil
}

// publish reescribe en el producto los precios locales por los publicados (variantes y opciones
// de modificadores). Lo usan solo las lecturas públicas.
func publish(r pricing.Rates, d *ProductDetail) {
	for i := range d.Variants {
		d.Variants[i].PriceCents = int32(r.PriceUnit(int64(d.Variants[i].PriceCents), nil, d.PromoDiscountBps).TotalCents) //nolint:gosec
	}
	for gi := range d.ModifierGroups {
		opts := d.ModifierGroups[gi].Options
		for oi := range opts {
			opts[oi].PriceDeltaCents = int32(r.PriceUnit(int64(opts[oi].PriceDeltaCents), nil, d.PromoDiscountBps).TotalCents) //nolint:gosec
		}
	}
}

// PublicGetProductDetail devuelve un producto con sus variantes y grupos de modificadores
// completos — lo que el cliente necesita para configurarlo antes de agregarlo al carrito. Mismo
// gate que el carrito (tenant.Gate.OpenForCustomer): la organización debe existir y estar
// activa, sin exigir ningún permiso — un cliente no es miembro del negocio.
func (s *Service) PublicGetProductDetail(ctx context.Context, orgID, id uuid.UUID) (ProductDetail, error) {
	if _, err := s.gate.OpenForCustomer(ctx, orgID); err != nil {
		return ProductDetail{}, err
	}
	p, err := s.repo.GetProduct(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) || (err == nil && !p.IsActive) {
		return ProductDetail{}, apperr.NotFound("producto no encontrado")
	}
	if err != nil {
		return ProductDetail{}, err
	}
	groups, err := s.repo.GetModifierGroupsByIDs(ctx, orgID, p.ModifierGroupIDs)
	if err != nil {
		return ProductDetail{}, err
	}
	r, err := s.pricer.RatesFor(ctx, orgID)
	if err != nil {
		return ProductDetail{}, err
	}
	out := ProductDetail{Product: p, ModifierGroups: groups}
	publish(r, &out)
	return out, nil
}

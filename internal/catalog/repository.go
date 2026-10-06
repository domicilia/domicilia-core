package catalog

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/domicilia/domicilia-core/internal/platform/db"
	"github.com/domicilia/domicilia-core/internal/store"
)

type pgRepository struct {
	q    *store.Queries
	pool *pgxpool.Pool
}

// NewRepository crea el repositorio sobre Postgres.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{q: store.New(pool), pool: pool}
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	return &n.UUID
}

func nullUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

// ---------------------------------------------------------------------------
// Categorías
// ---------------------------------------------------------------------------

func toCategory(c store.Category) Category {
	return Category{
		ID: c.ID, OrganizationID: c.OrganizationID, Name: c.Name,
		Position: int(c.Position), IsActive: c.IsActive, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func (r *pgRepository) InsertCategory(ctx context.Context, orgID uuid.UUID, name string, position int) (Category, error) {
	c, err := r.q.InsertCategory(ctx, store.InsertCategoryParams{OrganizationID: orgID, Name: name, Position: int32(position)}) //nolint:gosec // posición la acota el cliente
	if db.IsUniqueViolation(err) {
		return Category{}, ErrDuplicateName
	}
	if err != nil {
		return Category{}, fmt.Errorf("catalog: crear categoría: %w", err)
	}
	return toCategory(c), nil
}

func (r *pgRepository) GetCategory(ctx context.Context, orgID, id uuid.UUID) (Category, error) {
	c, err := r.q.GetCategory(ctx, store.GetCategoryParams{ID: id, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return Category{}, ErrNotFound
	}
	if err != nil {
		return Category{}, fmt.Errorf("catalog: leer categoría: %w", err)
	}
	return toCategory(c), nil
}

func (r *pgRepository) ListCategories(ctx context.Context, orgID uuid.UUID) ([]Category, error) {
	rows, err := r.q.ListCategories(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("catalog: listar categorías: %w", err)
	}
	out := make([]Category, len(rows))
	for i, c := range rows {
		out[i] = toCategory(c)
	}
	return out, nil
}

func (r *pgRepository) UpdateCategory(ctx context.Context, orgID, id uuid.UUID, p CategoryPatch) (Category, error) {
	var position *int32
	if p.Position != nil {
		v := int32(*p.Position) //nolint:gosec // posición la acota el cliente
		position = &v
	}
	c, err := r.q.UpdateCategory(ctx, store.UpdateCategoryParams{
		ID: id, OrganizationID: orgID, Name: p.Name, Position: position, IsActive: p.IsActive,
	})
	if db.IsNoRows(err) {
		return Category{}, ErrNotFound
	}
	if db.IsUniqueViolation(err) {
		return Category{}, ErrDuplicateName
	}
	if err != nil {
		return Category{}, fmt.Errorf("catalog: actualizar categoría: %w", err)
	}
	return toCategory(c), nil
}

// ---------------------------------------------------------------------------
// Productos
// ---------------------------------------------------------------------------

func toVariant(v store.ProductVariant) Variant {
	return Variant{
		ID: v.ID, Name: v.Name, PriceCents: v.PriceCents, IsDefault: v.IsDefault,
		IsActive: v.IsActive, Position: int(v.Position),
	}
}

func toProductBase(p store.Product) Product {
	ingredients, channels := p.Ingredients, p.Channels
	if ingredients == nil {
		ingredients = []string{}
	}
	if channels == nil {
		channels = []string{}
	}
	return Product{
		ID: p.ID, OrganizationID: p.OrganizationID, CategoryID: uuidPtr(p.CategoryID),
		Name: p.Name, Description: p.Description, ImageURL: p.ImageUrl,
		Position: int(p.Position), IsActive: p.IsActive, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		Ingredients: ingredients, Channels: channels,
		// Nunca nil: la mayoría de los productos no tiene grupos de modificadores, y un slice
		// nil sale como `null` en JSON, no `[]` — el contrato OpenAPI exige array, no null.
		Variants:         []Variant{},
		ModifierGroupIDs: []uuid.UUID{},
	}
}

// hydrateProducts completa Variants y ModifierGroupIDs de varios productos con dos consultas
// (no una por producto): ver el comentario de ListVariantsByProducts.
func (r *pgRepository) hydrateProducts(ctx context.Context, q *store.Queries, products []Product) ([]Product, error) {
	if len(products) == 0 {
		return products, nil
	}
	ids := make([]uuid.UUID, len(products))
	byID := make(map[uuid.UUID]int, len(products))
	for i, p := range products {
		ids[i] = p.ID
		byID[p.ID] = i
	}
	variants, err := q.ListVariantsByProducts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("catalog: listar variantes: %w", err)
	}
	for _, v := range variants {
		i := byID[v.ProductID]
		products[i].Variants = append(products[i].Variants, toVariant(v))
	}
	groups, err := q.ListModifierGroupIDsByProducts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("catalog: listar grupos de modificadores del producto: %w", err)
	}
	for _, g := range groups {
		i := byID[g.ProductID]
		products[i].ModifierGroupIDs = append(products[i].ModifierGroupIDs, g.ModifierGroupID)
	}
	return products, nil
}

// writeVariants reemplaza el conjunto de variantes de un producto: hace upsert de las que traen
// nombre/precio/id y desactiva (nunca borra) las que ya no vienen en la lista.
// writeVariants hace el upsert y devuelve TODAS las variantes del producto, incluidas las que
// acaban de desactivarse — no arma la respuesta a mano con lo que vino en `in`, la vuelve a leer:
// así la respuesta es lo que de verdad quedó en la base, no lo que el código cree que quedó.
func writeVariants(ctx context.Context, q *store.Queries, orgID, productID uuid.UUID, in []VariantInput) ([]Variant, error) {
	keep := make([]uuid.UUID, 0, len(in))
	for i, v := range in {
		if v.ID != nil {
			row, err := q.UpdateVariant(ctx, store.UpdateVariantParams{
				ID: *v.ID, ProductID: productID, OrganizationID: orgID,
				Name: v.Name, PriceCents: v.PriceCents, IsDefault: v.IsDefault, Position: int32(i), //nolint:gosec
			})
			if db.IsNoRows(err) {
				return nil, apperrVariantNotFound(*v.ID)
			}
			if err != nil {
				return nil, fmt.Errorf("catalog: actualizar variante: %w", err)
			}
			keep = append(keep, row.ID)
			continue
		}
		row, err := q.InsertVariant(ctx, store.InsertVariantParams{
			ProductID: productID, OrganizationID: orgID,
			Name: v.Name, PriceCents: v.PriceCents, IsDefault: v.IsDefault, Position: int32(i), //nolint:gosec
		})
		if err != nil {
			return nil, fmt.Errorf("catalog: crear variante: %w", err)
		}
		keep = append(keep, row.ID)
	}
	if err := q.DeactivateOtherVariants(ctx, store.DeactivateOtherVariantsParams{
		ProductID: productID, OrganizationID: orgID, KeepIds: keep,
	}); err != nil {
		return nil, fmt.Errorf("catalog: desactivar variantes retiradas: %w", err)
	}
	rows, err := q.ListVariantsByProducts(ctx, []uuid.UUID{productID})
	if err != nil {
		return nil, fmt.Errorf("catalog: releer variantes: %w", err)
	}
	out := make([]Variant, len(rows))
	for i, row := range rows {
		out[i] = toVariant(row)
	}
	return out, nil
}

func writeProductModifierGroups(ctx context.Context, q *store.Queries, orgID, productID uuid.UUID, groupIDs []uuid.UUID) error {
	if err := q.DeleteProductModifierGroups(ctx, store.DeleteProductModifierGroupsParams{
		ProductID: productID, OrganizationID: orgID,
	}); err != nil {
		return fmt.Errorf("catalog: soltar grupos de modificadores: %w", err)
	}
	for i, gid := range groupIDs {
		if err := q.InsertProductModifierGroup(ctx, store.InsertProductModifierGroupParams{
			ProductID: productID, OrganizationID: orgID, ModifierGroupID: gid, Position: int32(i), //nolint:gosec
		}); err != nil {
			// El grupo no existe, o es de otra organización (la FK es compuesta con
			// organization_id): en ambos casos, para quien llama es "ese id no existe".
			if db.IsForeignKeyViolation(err) {
				return fmt.Errorf("catalog: grupo de modificadores %s no existe: %w", gid, ErrNotFound)
			}
			return fmt.Errorf("catalog: asociar grupo de modificadores: %w", err)
		}
	}
	return nil
}

func (r *pgRepository) InsertProduct(ctx context.Context, n NewProduct) (Product, error) {
	var out Product
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		ingredients, channels := n.Ingredients, n.Channels
		if ingredients == nil {
			ingredients = []string{}
		}
		if channels == nil {
			channels = []string{}
		}
		row, err := q.InsertProduct(ctx, store.InsertProductParams{
			OrganizationID: n.OrganizationID, CategoryID: nullUUID(n.CategoryID), Name: n.Name,
			Description: n.Description, ImageUrl: n.ImageURL, Position: int32(n.Position), //nolint:gosec
			Ingredients: ingredients, Channels: channels,
		})
		if db.IsForeignKeyViolation(err) {
			return ErrCategoryNotFound
		}
		if err != nil {
			return fmt.Errorf("catalog: crear producto: %w", err)
		}
		out = toProductBase(row)
		if out.Variants, err = writeVariants(ctx, q, n.OrganizationID, out.ID, n.Variants); err != nil {
			return err
		}
		if err := writeProductModifierGroups(ctx, q, n.OrganizationID, out.ID, n.ModifierGroupIDs); err != nil {
			return err
		}
		out.ModifierGroupIDs = n.ModifierGroupIDs
		return nil
	})
	return out, err
}

func (r *pgRepository) GetProduct(ctx context.Context, orgID, id uuid.UUID) (Product, error) {
	row, err := r.q.GetProduct(ctx, store.GetProductParams{ID: id, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return Product{}, ErrNotFound
	}
	if err != nil {
		return Product{}, fmt.Errorf("catalog: leer producto: %w", err)
	}
	products, err := r.hydrateProducts(ctx, r.q, []Product{toProductBase(row)})
	if err != nil {
		return Product{}, err
	}
	return products[0], nil
}

func (r *pgRepository) ListProducts(ctx context.Context, orgID uuid.UUID, categoryID *uuid.UUID, limit, offset int) ([]Product, int64, error) {
	rows, err := r.q.ListProducts(ctx, store.ListProductsParams{
		OrganizationID: orgID, CategoryID: nullUUID(categoryID),
		PageSize: int32(limit), PageOffset: int32(offset), //nolint:gosec // acotados por el paginador
	})
	if err != nil {
		return nil, 0, fmt.Errorf("catalog: listar productos: %w", err)
	}
	total, err := r.q.CountProducts(ctx, store.CountProductsParams{OrganizationID: orgID, CategoryID: nullUUID(categoryID)})
	if err != nil {
		return nil, 0, fmt.Errorf("catalog: contar productos: %w", err)
	}
	products := make([]Product, len(rows))
	for i, row := range rows {
		products[i] = toProductBase(row)
	}
	products, err = r.hydrateProducts(ctx, r.q, products)
	return products, total, err
}

func (r *pgRepository) UpdateProduct(ctx context.Context, orgID, id uuid.UUID, p ProductPatch) (Product, error) {
	var out Product
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		var position *int32
		if p.Position != nil {
			v := int32(*p.Position) //nolint:gosec
			position = &v
		}
		var ingredients, channels []string
		if p.Ingredients != nil {
			ingredients = *p.Ingredients
		}
		if p.Channels != nil {
			channels = *p.Channels
		}
		row, err := q.UpdateProduct(ctx, store.UpdateProductParams{
			ID: id, OrganizationID: orgID,
			SetCategory: p.SetCategory, CategoryID: nullUUID(p.CategoryID),
			Name: p.Name, SetDescription: p.SetDesc, Description: p.Description,
			SetImage: p.SetImage, ImageUrl: p.ImageURL, Position: position, IsActive: p.IsActive,
			SetIngredients: p.Ingredients != nil, Ingredients: ingredients,
			SetChannels: p.Channels != nil, Channels: channels,
		})
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		if db.IsForeignKeyViolation(err) {
			return ErrCategoryNotFound
		}
		if err != nil {
			return fmt.Errorf("catalog: actualizar producto: %w", err)
		}
		out = toProductBase(row)
		if p.Variants != nil {
			if out.Variants, err = writeVariants(ctx, q, orgID, id, *p.Variants); err != nil {
				return err
			}
		} else {
			rows, err := q.ListVariantsByProducts(ctx, []uuid.UUID{id})
			if err != nil {
				return fmt.Errorf("catalog: listar variantes: %w", err)
			}
			for _, v := range rows {
				out.Variants = append(out.Variants, toVariant(v))
			}
		}
		if p.ModifierGroupIDs != nil {
			if err := writeProductModifierGroups(ctx, q, orgID, id, *p.ModifierGroupIDs); err != nil {
				return err
			}
			out.ModifierGroupIDs = *p.ModifierGroupIDs
		} else {
			rows, err := q.ListModifierGroupIDsByProducts(ctx, []uuid.UUID{id})
			if err != nil {
				return fmt.Errorf("catalog: listar grupos de modificadores: %w", err)
			}
			for _, g := range rows {
				out.ModifierGroupIDs = append(out.ModifierGroupIDs, g.ModifierGroupID)
			}
		}
		return nil
	})
	return out, err
}

// apperrVariantNotFound: una variante con ID que no pertenece a este producto/organización.
// Función aparte (no una constante) porque el mensaje incluye el id — ayuda a depurar un cliente
// que mandó un id de otro producto.
func apperrVariantNotFound(id uuid.UUID) error {
	return fmt.Errorf("catalog: variante %s no pertenece a este producto: %w", id, ErrNotFound)
}

// ---------------------------------------------------------------------------
// Grupos de modificadores
// ---------------------------------------------------------------------------

func toOption(o store.ModifierOption) ModifierOption {
	return ModifierOption{
		ID: o.ID, Name: o.Name, PriceDeltaCents: o.PriceDeltaCents,
		IsActive: o.IsActive, Position: int(o.Position),
	}
}

func toModifierGroupBase(g store.ModifierGroup) ModifierGroup {
	return ModifierGroup{
		ID: g.ID, OrganizationID: g.OrganizationID, Name: g.Name, SelectionType: g.SelectionType,
		MinSelect: int(g.MinSelect), MaxSelect: int(g.MaxSelect), IsActive: g.IsActive,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
		Options: []ModifierOption{}, // nunca nil, mismo motivo que Product.Variants
	}
}

func (r *pgRepository) hydrateGroups(ctx context.Context, q *store.Queries, groups []ModifierGroup) ([]ModifierGroup, error) {
	if len(groups) == 0 {
		return groups, nil
	}
	ids := make([]uuid.UUID, len(groups))
	byID := make(map[uuid.UUID]int, len(groups))
	for i, g := range groups {
		ids[i] = g.ID
		byID[g.ID] = i
	}
	options, err := q.ListModifierOptionsByGroups(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("catalog: listar opciones: %w", err)
	}
	for _, o := range options {
		i := byID[o.ModifierGroupID]
		groups[i].Options = append(groups[i].Options, toOption(o))
	}
	return groups, nil
}

// writeOptions hace el upsert y devuelve TODAS las opciones del grupo, incluidas las que acaban
// de desactivarse — mismo motivo que writeVariants: se relee de la base, no se arma a mano.
func writeOptions(ctx context.Context, q *store.Queries, orgID, groupID uuid.UUID, in []OptionInput) ([]ModifierOption, error) {
	keep := make([]uuid.UUID, 0, len(in))
	for i, o := range in {
		if o.ID != nil {
			row, err := q.UpdateModifierOption(ctx, store.UpdateModifierOptionParams{
				ID: *o.ID, ModifierGroupID: groupID, OrganizationID: orgID,
				Name: o.Name, PriceDeltaCents: o.PriceDeltaCents, Position: int32(i), //nolint:gosec
			})
			if db.IsNoRows(err) {
				return nil, fmt.Errorf("catalog: opción %s no pertenece a este grupo: %w", *o.ID, ErrNotFound)
			}
			if err != nil {
				return nil, fmt.Errorf("catalog: actualizar opción: %w", err)
			}
			keep = append(keep, row.ID)
			continue
		}
		row, err := q.InsertModifierOption(ctx, store.InsertModifierOptionParams{
			ModifierGroupID: groupID, OrganizationID: orgID,
			Name: o.Name, PriceDeltaCents: o.PriceDeltaCents, Position: int32(i), //nolint:gosec
		})
		if err != nil {
			return nil, fmt.Errorf("catalog: crear opción: %w", err)
		}
		keep = append(keep, row.ID)
	}
	if err := q.DeactivateOtherModifierOptions(ctx, store.DeactivateOtherModifierOptionsParams{
		ModifierGroupID: groupID, OrganizationID: orgID, KeepIds: keep,
	}); err != nil {
		return nil, fmt.Errorf("catalog: desactivar opciones retiradas: %w", err)
	}
	rows, err := q.ListModifierOptionsByGroups(ctx, []uuid.UUID{groupID})
	if err != nil {
		return nil, fmt.Errorf("catalog: releer opciones: %w", err)
	}
	out := make([]ModifierOption, len(rows))
	for i, row := range rows {
		out[i] = toOption(row)
	}
	return out, nil
}

func (r *pgRepository) InsertModifierGroup(ctx context.Context, n NewModifierGroup) (ModifierGroup, error) {
	var out ModifierGroup
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		row, err := q.InsertModifierGroup(ctx, store.InsertModifierGroupParams{
			OrganizationID: n.OrganizationID, Name: n.Name, SelectionType: n.SelectionType,
			MinSelect: int32(n.MinSelect), MaxSelect: int32(n.MaxSelect), //nolint:gosec
		})
		if db.IsUniqueViolation(err) {
			return ErrDuplicateName
		}
		if err != nil {
			return fmt.Errorf("catalog: crear grupo de modificadores: %w", err)
		}
		out = toModifierGroupBase(row)
		out.Options, err = writeOptions(ctx, q, n.OrganizationID, out.ID, n.Options)
		return err
	})
	return out, err
}

func (r *pgRepository) GetModifierGroup(ctx context.Context, orgID, id uuid.UUID) (ModifierGroup, error) {
	row, err := r.q.GetModifierGroup(ctx, store.GetModifierGroupParams{ID: id, OrganizationID: orgID})
	if db.IsNoRows(err) {
		return ModifierGroup{}, ErrNotFound
	}
	if err != nil {
		return ModifierGroup{}, fmt.Errorf("catalog: leer grupo de modificadores: %w", err)
	}
	groups, err := r.hydrateGroups(ctx, r.q, []ModifierGroup{toModifierGroupBase(row)})
	if err != nil {
		return ModifierGroup{}, err
	}
	return groups[0], nil
}

func (r *pgRepository) ListModifierGroups(ctx context.Context, orgID uuid.UUID) ([]ModifierGroup, error) {
	rows, err := r.q.ListModifierGroups(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("catalog: listar grupos de modificadores: %w", err)
	}
	groups := make([]ModifierGroup, len(rows))
	for i, row := range rows {
		groups[i] = toModifierGroupBase(row)
	}
	return r.hydrateGroups(ctx, r.q, groups)
}

func (r *pgRepository) GetModifierGroupsByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]ModifierGroup, error) {
	if len(ids) == 0 {
		return []ModifierGroup{}, nil
	}
	rows, err := r.q.GetModifierGroupsByIDs(ctx, store.GetModifierGroupsByIDsParams{Ids: ids, OrganizationID: orgID})
	if err != nil {
		return nil, fmt.Errorf("catalog: leer grupos de modificadores: %w", err)
	}
	groups := make([]ModifierGroup, len(rows))
	for i, row := range rows {
		groups[i] = toModifierGroupBase(row)
	}
	return r.hydrateGroups(ctx, r.q, groups)
}

func (r *pgRepository) UpdateModifierGroup(ctx context.Context, orgID, id uuid.UUID, p ModifierGroupPatch) (ModifierGroup, error) {
	var out ModifierGroup
	err := db.InTx(ctx, r.pool, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		var minSel, maxSel *int32
		if p.MinSelect != nil {
			v := int32(*p.MinSelect) //nolint:gosec
			minSel = &v
		}
		if p.MaxSelect != nil {
			v := int32(*p.MaxSelect) //nolint:gosec
			maxSel = &v
		}
		row, err := q.UpdateModifierGroup(ctx, store.UpdateModifierGroupParams{
			ID: id, OrganizationID: orgID, Name: p.Name, SelectionType: p.SelectionType,
			MinSelect: minSel, MaxSelect: maxSel, IsActive: p.IsActive,
		})
		if db.IsNoRows(err) {
			return ErrNotFound
		}
		if db.IsUniqueViolation(err) {
			return ErrDuplicateName
		}
		if err != nil {
			return fmt.Errorf("catalog: actualizar grupo de modificadores: %w", err)
		}
		out = toModifierGroupBase(row)
		if p.Options != nil {
			out.Options, err = writeOptions(ctx, q, orgID, id, *p.Options)
			return err
		}
		rows, err := q.ListModifierOptionsByGroups(ctx, []uuid.UUID{id})
		if err != nil {
			return fmt.Errorf("catalog: listar opciones: %w", err)
		}
		for _, o := range rows {
			out.Options = append(out.Options, toOption(o))
		}
		return nil
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Feed público
// ---------------------------------------------------------------------------

func toFeedProduct(r store.ListPublicProductsRow) FeedProduct {
	return FeedProduct{
		ID: r.ID, OrganizationID: r.OrganizationID, OrganizationName: r.OrganizationName,
		OrganizationSlug: r.OrganizationSlug, OrganizationLogoURL: r.OrganizationLogoUrl,
		CategoryID: uuidPtr(r.CategoryID), CategoryName: r.CategoryName,
		Name: r.Name, Description: r.Description, ImageURL: r.ImageUrl,
		MinPriceCents: r.MinPriceCents,
	}
}

func (r *pgRepository) ListPublicProducts(ctx context.Context, filter PublicFeedFilter, limit, offset int) ([]FeedProduct, int64, error) {
	orgID := nullUUID(filter.OrganizationID)
	rows, err := r.q.ListPublicProducts(ctx, store.ListPublicProductsParams{
		OrganizationID: orgID, OrganizationSlug: filter.OrganizationSlug, Search: filter.Search, Category: filter.Category,
		PageSize: int32(limit), PageOffset: int32(offset), //nolint:gosec // acotados por el paginador
	})
	if err != nil {
		return nil, 0, fmt.Errorf("catalog: listar el feed público: %w", err)
	}
	total, err := r.q.CountPublicProducts(ctx, store.CountPublicProductsParams{
		OrganizationID: orgID, OrganizationSlug: filter.OrganizationSlug, Search: filter.Search, Category: filter.Category,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("catalog: contar el feed público: %w", err)
	}
	out := make([]FeedProduct, len(rows))
	for i, row := range rows {
		out[i] = toFeedProduct(row)
	}
	return out, total, nil
}

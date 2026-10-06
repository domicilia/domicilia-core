package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Códigos SQLSTATE de Postgres que los repositorios traducen a errores de negocio.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

// InTx ejecuta fn dentro de una transacción: confirma si devuelve nil y revierte
// en cualquier otro caso (incluido un panic).
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) (err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: iniciar transacción: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			// Un rollback que falla no debe tapar el error original.
			_ = tx.Rollback(ctx)
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: confirmar transacción: %w", err)
	}
	return nil
}

// IsUniqueViolation dice si err viola una restricción UNIQUE o PRIMARY KEY.
func IsUniqueViolation(err error) bool { return hasCode(err, pgUniqueViolation) }

// IsForeignKeyViolation dice si err viola una clave foránea.
func IsForeignKeyViolation(err error) bool { return hasCode(err, pgForeignKeyViolation) }

// ConstraintName devuelve el nombre de la restricción que violó err ("" si no es
// un error de restricción). Sirve para dar un mensaje distinto según cuál sea:
// users_pkey (el perfil ya existe) o ix_users_email (correo ya registrado).
func ConstraintName(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.ConstraintName
	}
	return ""
}

func hasCode(err error, code string) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == code
}

// IsNoRows dice si err es "sin filas" (consulta :one sin resultado).
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// likeEscaper escapa los comodines de LIKE/ILIKE (la barra invertida es el escape por omisión de
// Postgres) para que una búsqueda que contiene % o _ se tome literal.
var likeEscaper = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")

// EscapeLike prepara un texto de búsqueda para usarlo dentro de un patrón LIKE.
func EscapeLike(s string) string { return likeEscaper.Replace(s) }

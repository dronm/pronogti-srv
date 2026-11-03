// Package salon maps public salon URLs to YClients companies.
package salon

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/dronm/pronogti/internal/models"
)

var ErrSalonNotFound = errors.New("salon not found")

type Entry struct {
	models.SalonIdentity
	IsActive       bool  `json:"-"`
	SessionVersion int64 `json:"-"`
}

type Registry interface {
	List(context.Context) ([]Entry, error)
	FindBySlug(context.Context, string) (Entry, error)
	FindByCompanyID(context.Context, int64) (Entry, error)
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func ValidSlug(slug string) bool {
	return len(slug) >= 1 && len(slug) <= 100 && slugPattern.MatchString(slug)
}

// Validate checks operator-provided values before a database connection is used.
func Validate(companyID int64, slug, name string) error {
	if companyID <= 0 {
		return errors.New("positive YClients company ID is required")
	}
	if !ValidSlug(slug) {
		return errors.New("salon slug must contain 1 to 100 lowercase letters, digits, and single separating hyphens")
	}
	length := utf8.RuneCountInString(strings.TrimSpace(name))
	if length < 1 || length > 200 {
		return errors.New("salon name must contain 1 to 200 characters")
	}
	return nil
}

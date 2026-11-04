package adminnav

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dronm/ds/v4"
	"github.com/dronm/modelbind"
	"github.com/dronm/modelbind/types"
	"github.com/dronm/pronogti/internal/adminauth"
	"github.com/dronm/webapp"
	wmodels "github.com/dronm/webapp/models"
)

type Service struct{ db ds.Provider }

func NewService(db ds.Provider) *Service { return &Service{db: db} }

func (s *Service) connection(ctx context.Context) (ds.Conn, func(), error) {
	if s == nil || s.db == nil {
		return nil, nil, errors.New("administrator navigation database is not configured")
	}
	lease, server, err := s.db.GetPrimary(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire administrator navigation database: %w", err)
	}
	return lease.Conn(), func() { s.db.Release(lease, server) }, nil
}

func normalizeDBError(err error) error {
	var pg interface{ SQLState() string }
	if errors.As(err, &pg) {
		switch pg.SQLState() {
		case "23503", "23514", "23502", "22001", "22P02":
			return invalid("menu hierarchy, owner or route reference is invalid")
		case "23505":
			return invalid("this navigation identifier is already registered")
		}
	}
	return err
}

func (s *Service) write(ctx context.Context, fn func(context.Context, ds.Tx) error) error {
	conn, release, err := s.connection(ctx)
	if err != nil {
		return err
	}
	defer release()
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1886544231,1)`); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
	return normalizeDBError(err)
}

func collection[T types.DBAggModel](ctx context.Context, s *Service, model T, params modelbind.CollectionParams, maxCount int) (wmodels.CollectionResponse[T], error) {
	result := wmodels.CollectionResponse[T]{}
	if params.From < 0 || params.Count < 0 || int(params.Count) > maxCount || len(params.Sorter) > 20 || len(params.Filter) > 50 {
		return result, invalid("invalid collection limits")
	}
	if params.Count == 0 {
		params.Count = 100
	}
	if len(params.Sorter) == 0 {
		params.Sorter = []modelbind.CollectionSorter{{Field: "id", Direct: modelbind.SortParAsc}}
	}
	conn, release, err := s.connection(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	// Use the primary for immediately consistent navigation after an edit. The
	// framework resolves browser filter/sort names against model metadata.
	rows, agg, err := webapp.FetchCollectionModelWithConn(ctx, conn, model, params)
	if err != nil {
		var message *modelbind.MessageError
		var validation *modelbind.ValidationError
		if errors.As(err, &message) || errors.As(err, &validation) {
			return result, invalid("invalid collection filter or sort field")
		}
		return result, err
	}
	return wmodels.CollectionResponse[T]{Rows: rows, Agg: agg}, nil
}

func (s *Service) ListMenus(ctx context.Context, params modelbind.CollectionParams) (wmodels.CollectionResponse[*MainMenuList], error) {
	return collection(ctx, s, &MainMenuList{}, params, 10000)
}

func (s *Service) ListRoutes(ctx context.Context, params modelbind.CollectionParams) (wmodels.CollectionResponse[*ApplicationRoute], error) {
	return collection(ctx, s, &ApplicationRoute{}, params, 10000)
}

func (s *Service) ListUsers(ctx context.Context, params modelbind.CollectionParams) (wmodels.CollectionResponse[*UserReference], error) {
	return collection(ctx, s, &UserReference{}, params, 200)
}

const menuColumns = `id,role_id::text,user_id,parent_id,caption,route_id,icon,sort_order,is_active,created_at,updated_at`
const routeColumns = `id,name,path,descr,section,icon,menu_available,is_active,created_at,updated_at`

func scanMenu(row ds.Row) (*MainMenu, error) {
	menu := &MainMenu{}
	err := row.Scan(&menu.ID, &menu.RoleID, &menu.UserID, &menu.ParentID, &menu.Caption, &menu.RouteID, &menu.Icon, &menu.SortOrder, &menu.IsActive, &menu.CreatedAt, &menu.UpdatedAt)
	if errors.Is(err, ds.ErrNoRows) {
		return nil, ErrNotFound
	}
	return menu, err
}

func scanRoute(row ds.Row) (*ApplicationRoute, error) {
	route := &ApplicationRoute{}
	err := row.Scan(&route.ID, &route.Name, &route.Path, &route.Descr, &route.Section, &route.Icon, &route.MenuAvailable, &route.IsActive, &route.CreatedAt, &route.UpdatedAt)
	if errors.Is(err, ds.ErrNoRows) {
		return nil, ErrNotFound
	}
	return route, err
}

func (s *Service) Menu(ctx context.Context, id int) (*MainMenu, error) {
	if id <= 0 {
		return nil, ErrNotFound
	}
	conn, release, err := s.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return scanMenu(conn.QueryRow(ctx, `SELECT `+menuColumns+` FROM public.main_menus WHERE id=$1`, id))
}

func (s *Service) Route(ctx context.Context, id int) (*ApplicationRoute, error) {
	if id <= 0 {
		return nil, ErrNotFound
	}
	conn, release, err := s.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return scanRoute(conn.QueryRow(ctx, `SELECT `+routeColumns+` FROM public.application_routes WHERE id=$1`, id))
}

func (s *Service) User(ctx context.Context, id int) (*UserReference, error) {
	if id <= 0 {
		return nil, ErrNotFound
	}
	conn, release, err := s.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	user := &UserReference{}
	err = conn.QueryRow(ctx, `SELECT id,name,role_id::text FROM public.admin_menu_users WHERE id=$1`, id).Scan(&user.ID, &user.Name, &user.RoleID)
	if errors.Is(err, ds.ErrNoRows) {
		return nil, ErrNotFound
	}
	return user, err
}

func (s *Service) CreateMenu(ctx context.Context, input MainMenu) (map[string]any, error) {
	if err := validateMenu(&input); err != nil {
		return nil, err
	}
	var id int
	err := s.write(ctx, func(ctx context.Context, tx ds.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO public.main_menus(role_id,user_id,parent_id,caption,route_id,icon,sort_order,is_active)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, input.RoleID, input.UserID, input.ParentID, input.Caption, input.RouteID, input.Icon, input.SortOrder, input.IsActive).Scan(&id)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id}, nil
}

func (s *Service) UpdateMenu(ctx context.Context, id int, patch Patch) (wmodels.RowsAffectedResponse, error) {
	result := wmodels.RowsAffectedResponse{}
	if id <= 0 {
		return result, ErrNotFound
	}
	err := s.write(ctx, func(ctx context.Context, tx ds.Tx) error {
		menu, err := scanMenu(tx.QueryRow(ctx, `SELECT `+menuColumns+` FROM public.main_menus WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if err = applyMenuPatch(menu, patch); err != nil {
			return err
		}
		updated, err := tx.Exec(ctx, `UPDATE public.main_menus SET role_id=$2,user_id=$3,parent_id=$4,caption=$5,route_id=$6,icon=$7,sort_order=$8,is_active=$9 WHERE id=$1`,
			id, menu.RoleID, menu.UserID, menu.ParentID, menu.Caption, menu.RouteID, menu.Icon, menu.SortOrder, menu.IsActive)
		if err == nil {
			result.RowsAffected = updated.RowsAffected()
		}
		return err
	})
	return result, err
}

func (s *Service) DeleteMenu(ctx context.Context, id int) (wmodels.RowsAffectedResponse, error) {
	result := wmodels.RowsAffectedResponse{}
	if id <= 0 {
		return result, ErrNotFound
	}
	err := s.write(ctx, func(ctx context.Context, tx ds.Tx) error {
		deleted, err := tx.Exec(ctx, `DELETE FROM public.main_menus WHERE id=$1`, id)
		if err != nil {
			return err
		}
		result.RowsAffected = deleted.RowsAffected()
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
	return result, err
}

func (s *Service) UpdateRoute(ctx context.Context, id int, patch Patch) (wmodels.RowsAffectedResponse, error) {
	result := wmodels.RowsAffectedResponse{}
	if id <= 0 {
		return result, ErrNotFound
	}
	err := s.write(ctx, func(ctx context.Context, tx ds.Tx) error {
		route, err := scanRoute(tx.QueryRow(ctx, `SELECT `+routeColumns+` FROM public.application_routes WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if err = applyRoutePatch(route, patch); err != nil {
			return err
		}
		updated, err := tx.Exec(ctx, `UPDATE public.application_routes SET descr=$2,section=$3,icon=$4 WHERE id=$1`, id, route.Descr, route.Section, route.Icon)
		if err == nil {
			result.RowsAffected = updated.RowsAffected()
		}
		return err
	})
	return result, err
}

func (s *Service) SyncRoutes(ctx context.Context, input ApplicationRouteSyncInput) (ApplicationRouteSyncResult, error) {
	result := ApplicationRouteSyncResult{}
	input, err := validateManifest(input)
	if err != nil {
		return result, err
	}
	err = s.write(ctx, func(ctx context.Context, tx ds.Tx) error {
		names := make([]string, 0, len(input.Items))
		for _, item := range input.Items {
			// Metadata edits survive future logins and manifest synchronization.
			_, err := tx.Exec(ctx, `INSERT INTO public.application_routes(name,path,descr,section,icon,menu_available)
				VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(name) DO UPDATE
				SET path=EXCLUDED.path,menu_available=EXCLUDED.menu_available,is_active=true`, item.Name, item.Path, item.Descr, item.Section, item.Icon, item.MenuAvailable)
			if err != nil {
				return err
			}
			names = append(names, item.Name)
		}
		deactivated, err := tx.Exec(ctx, `UPDATE public.application_routes SET is_active=false WHERE is_active AND NOT(name=ANY($1::text[]))`, names)
		if err == nil {
			result.Registered = len(names)
			result.Deactivated = int(deactivated.RowsAffected())
		}
		return err
	})
	return result, err
}

func (s *Service) RouteAutocomplete(ctx context.Context, query string, limit int) ([]*ApplicationRouteOption, error) {
	query = strings.TrimSpace(query)
	if len(query) > 500 {
		return nil, invalid("route search is too long")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		return nil, invalid("route autocomplete limit must not exceed 100")
	}
	conn, release, err := s.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := conn.Query(ctx, `SELECT id,name,path,descr,section,icon FROM public.application_routes
		WHERE is_active AND menu_available AND ($1='' OR position(lower($1) in lower(descr||' '||name||' '||section))>0)
		ORDER BY section,descr,id LIMIT $2`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*ApplicationRouteOption, 0)
	for rows.Next() {
		option := &ApplicationRouteOption{}
		if err := rows.Scan(&option.ID, &option.Name, &option.Path, &option.Descr, &option.Section, &option.Icon); err != nil {
			return nil, err
		}
		result = append(result, option)
	}
	return result, rows.Err()
}

type menuTreeRow struct {
	item     *MenuItem
	parentID *int
	userID   *int
}

func menuTree(rows []menuTreeRow, userID int64) []*MenuItem {
	personal := false
	for _, row := range rows {
		if row.parentID == nil && row.userID != nil && int64(*row.userID) == userID {
			personal = true
			break
		}
	}
	items := make(map[int]*MenuItem, len(rows))
	selected := make([]menuTreeRow, 0, len(rows))
	for _, row := range rows {
		if personal && (row.userID == nil || int64(*row.userID) != userID) {
			continue
		}
		if !personal && row.userID != nil {
			continue
		}
		row.item.Children = make([]*MenuItem, 0)
		items[row.item.ID] = row.item
		selected = append(selected, row)
	}
	result := make([]*MenuItem, 0)
	for _, row := range selected {
		if row.parentID == nil {
			result = append(result, row.item)
		} else if parent := items[*row.parentID]; parent != nil {
			parent.Children = append(parent.Children, row.item)
		}
	}
	return result
}

func (s *Service) ForUser(ctx context.Context, user adminauth.User) ([]*MenuItem, error) {
	if user.ID <= 0 || user.RoleID != "admin" {
		return nil, invalid("administrator identity is required")
	}
	conn, release, err := s.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := conn.Query(ctx, `SELECT m.id,m.parent_id,m.user_id,m.caption,COALESCE(ar.name,''),m.icon
		FROM public.main_menus m LEFT JOIN public.application_routes ar ON ar.id=m.route_id
		WHERE m.is_active AND (m.user_id=$1 OR m.role_id='admin')
		AND (m.route_id IS NULL OR (ar.is_active AND ar.menu_available)) ORDER BY m.sort_order,m.id`, user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	data := make([]menuTreeRow, 0)
	for rows.Next() {
		row := menuTreeRow{item: &MenuItem{}}
		if err := rows.Scan(&row.item.ID, &row.parentID, &row.userID, &row.item.Caption, &row.item.Route, &row.item.Icon); err != nil {
			return nil, err
		}
		data = append(data, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return menuTree(data, user.ID), nil
}

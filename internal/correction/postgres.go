package correction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dronm/ds/v4"
	"github.com/dronm/webapp"
	"github.com/google/uuid"
)

type PostgresRepository struct{ db ds.Provider }

func NewPostgresRepository(db ds.Provider) *PostgresRepository { return &PostgresRepository{db: db} }

const correctionColumns = `c.id::text,c.order_id::text,c.payment_id::text,c.company_id,c.environment,c.operator_id,
	c.source_version,c.reason,c.basis,c.calculation_date::text,c.prescription_number,c.state,c.original_identity,
	c.reversal_payload,c.replacement_payload,c.fiscal_snapshot,c.created_at,c.updated_at`

func readCorrection(row ds.Row) (Correction, error) {
	var c Correction
	var identity, reversal, replacement, profile []byte
	err := row.Scan(&c.ID, &c.OrderID, &c.PaymentID, &c.CompanyID, &c.Environment, &c.OperatorID,
		&c.SourceVersion, &c.Reason, &c.Basis, &c.CalculationDate, &c.PrescriptionNumber, &c.State, &identity,
		&reversal, &replacement, &profile, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, err
	}
	for _, part := range []struct {
		raw  []byte
		dest any
	}{{identity, &c.Original}, {reversal, &c.Reversal}, {replacement, &c.Replacement}, {profile, &c.Profile}} {
		if err := json.Unmarshal(part.raw, part.dest); err != nil {
			return c, fmt.Errorf("decode correction snapshot: %w", err)
		}
	}
	c.Documents = []Document{}
	return c, nil
}

const documentColumns = `d.id::text,d.role,d.attempt,d.request_id,d.state,COALESCE(d.error_code,''),d.response_payload,d.initial_response_payload,d.created_at,d.updated_at,d.payload,d.dispatch_started_at`

func readDocument(row ds.Row, extra ...any) (Document, error) {
	var d Document
	dest := []any{&d.ID, &d.Role, &d.Attempt, &d.RequestID, &d.State, &d.ErrorCode, &d.Response, &d.InitialResponse, &d.CreatedAt, &d.UpdatedAt, &d.Payload, &d.DispatchStartedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return d, err
	}
	if d.State == "issued" {
		identity, err := responseIdentity(d.Response)
		if err != nil {
			return d, err
		}
		d.Identity = &identity
	}
	return d, nil
}

func responseIdentity(raw []byte) (FiscalIdentity, error) {
	var result struct {
		Data struct {
			Receipt struct {
				DocumentNumber uint32 `json:"documentNumber"`
				FiscalSign     uint32 `json:"fiscalSign"`
				DateTime       string `json:"dateTime"`
				ReceiptNumber  uint16 `json:"receiptNumber"`
				ShiftNumber    uint16 `json:"shiftNumber"`
			} `json:"receipt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return FiscalIdentity{}, err
	}
	x := result.Data.Receipt
	if x.DocumentNumber == 0 || x.FiscalSign == 0 || x.DateTime == "" {
		return FiscalIdentity{}, errors.New("issued receipt has no fiscal identity")
	}
	return FiscalIdentity{DocumentNumber: x.DocumentNumber, FiscalSign: x.FiscalSign, DateTime: x.DateTime, ReceiptNumber: x.ReceiptNumber, ShiftNumber: x.ShiftNumber}, nil
}

func (r *PostgresRepository) Load(ctx context.Context, orderID string) (Source, []Correction, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Source{}, nil, err
	}
	defer release()
	source, err := loadSource(ctx, conn, orderID)
	if err != nil {
		return source, nil, err
	}
	history, err := loadCorrections(ctx, conn, " WHERE c.order_id=$1 ORDER BY c.created_at DESC,c.id DESC LIMIT 100", orderID)
	return source, history, err
}

func loadSource(ctx context.Context, q ds.Querier, orderID string) (Source, error) {
	var s Source
	var profile, response []byte
	var completedAt *time.Time
	var orderState string
	err := q.QueryRow(ctx, `SELECT o.id::text,o.company_id,COALESCE(fr.payment_id::text,''),COALESCE(fr.environment,''),
		COALESCE(fr.state,''),COALESCE(fr.fiscal_snapshot,o.fiscal_snapshot),fr.request_payload,fr.response_payload,fr.completed_at,o.state,
		EXISTS(SELECT 1 FROM public.payment_refunds r WHERE r.order_id=o.id AND r.bank_state<>'rejected'),
		EXISTS(SELECT 1 FROM public.fiscal_corrections c WHERE c.order_id=o.id AND c.state<>'completed')
		FROM public.payment_orders o LEFT JOIN public.fiscal_receipts fr ON fr.order_id=o.id WHERE o.id=$1`, orderID).Scan(
		&s.OrderID, &s.CompanyID, &s.PaymentID, &s.Environment, &s.State, &profile, &s.RawPayload, &response, &completedAt, &orderState, &s.HasRefunds, &s.HasOpenCorrection)
	if errors.Is(err, ds.ErrNoRows) {
		return s, webapp.NotFound("Заказ не найден.", nil)
	}
	if err != nil {
		return s, fmt.Errorf("load correction source: %w", err)
	}
	if err := json.Unmarshal(profile, &s.Profile); err != nil {
		return s, err
	}
	s.Version = "sale"
	// Only completed chains become the effective source. An unfinished chain
	// remains visible and blocks all new corrections and bank refunds.
	var latestID, calculationDate string
	var latestPayload, latestProfile, latestResponse []byte
	var latestState string
	err = q.QueryRow(ctx, `SELECT c.id::text,c.calculation_date::text,c.replacement_payload,c.fiscal_snapshot,d.response_payload,d.state
		FROM public.fiscal_corrections c JOIN public.fiscal_correction_documents d ON d.correction_id=c.id AND d.role='replacement'
		WHERE c.order_id=$1 AND c.state='completed' AND d.state IN ('issued','demo')
		ORDER BY c.created_at DESC,c.id DESC,d.attempt DESC LIMIT 1`, orderID).Scan(&latestID, &calculationDate, &latestPayload, &latestProfile, &latestResponse, &latestState)
	if err == nil {
		s.Version, s.RawPayload, s.CalculationDate, s.State = latestID, latestPayload, calculationDate, latestState
		profile, response = latestProfile, latestResponse
		if err := json.Unmarshal(profile, &s.Profile); err != nil {
			return s, err
		}
	} else if !errors.Is(err, ds.ErrNoRows) {
		return s, fmt.Errorf("load corrected source: %w", err)
	}
	if len(s.RawPayload) > 0 {
		s.Payload, err = DecodeReceipt(s.RawPayload)
		if err != nil {
			return s, conflict("Сохранённый чек содержит неподдерживаемые реквизиты. Автоматическая коррекция недоступна.")
		}
	}
	if s.State == "issued" {
		s.Identity, err = responseIdentity(response)
		if err != nil {
			return s, conflict("В исходном чеке отсутствуют подтверждённые фискальные реквизиты.")
		}
		if s.CalculationDate == "" && len(s.Identity.DateTime) >= 10 {
			s.CalculationDate = s.Identity.DateTime[:10]
		}
	} else if s.CalculationDate == "" && completedAt != nil {
		s.CalculationDate = completedAt.Format("2006-01-02")
	}
	if orderState != "paid" {
		s.State = "unpaid"
	}
	return s, nil
}

func loadDocuments(ctx context.Context, q ds.Querier, id string) ([]Document, error) {
	rows, err := q.Query(ctx, `SELECT `+documentColumns+` FROM public.fiscal_correction_documents d WHERE d.correction_id=$1 ORDER BY CASE d.role WHEN 'reversal' THEN 0 ELSE 1 END,d.attempt`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs := []Document{}
	for rows.Next() {
		d, err := readDocument(rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}
func loadCorrection(ctx context.Context, q ds.Querier, orderID, id string) (Correction, error) {
	c, err := readCorrection(q.QueryRow(ctx, `SELECT `+correctionColumns+` FROM public.fiscal_corrections c WHERE c.order_id=$1 AND c.id=$2`, orderID, id))
	if errors.Is(err, ds.ErrNoRows) {
		return c, webapp.NotFound("Коррекция не найдена.", nil)
	}
	if err != nil {
		return c, err
	}
	c.Documents, err = loadDocuments(ctx, q, c.ID)
	return c, err
}
func loadCorrections(ctx context.Context, q ds.Querier, suffix string, args ...any) ([]Correction, error) {
	rows, err := q.Query(ctx, `SELECT `+correctionColumns+` FROM public.fiscal_corrections c`+suffix, args...)
	if err != nil {
		return nil, err
	}
	items := []Correction{}
	for rows.Next() {
		c, err := readCorrection(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Documents, err = loadDocuments(ctx, q, items[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}
func (r *PostgresRepository) Get(ctx context.Context, orderID, id string) (Correction, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Correction{}, err
	}
	defer release()
	return loadCorrection(ctx, conn, orderID, id)
}
func (r *PostgresRepository) List(ctx context.Context) ([]Correction, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return loadCorrections(ctx, conn, " ORDER BY c.created_at DESC,c.id DESC LIMIT 200")
}

func lockOrder(ctx context.Context, tx ds.Tx, orderID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM public.payment_orders WHERE id=$1 FOR UPDATE`, orderID).Scan(&id)
	if errors.Is(err, ds.ErrNoRows) {
		return webapp.NotFound("Заказ не найден.", nil)
	}
	return err
}

func (r *PostgresRepository) Reserve(ctx context.Context, orderID string, operatorID int64, key, hash string, prepare func(Source) (Preview, error), request CreateRequest) (Correction, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Correction{}, err
	}
	defer release()
	var result Correction
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		if err := lockOrder(ctx, tx, orderID); err != nil {
			return err
		}
		var id, storedHash string
		err := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM public.fiscal_corrections WHERE order_id=$1 AND idempotency_key=$2`, orderID, key).Scan(&id, &storedHash)
		if err == nil {
			if hash != storedHash {
				return conflict("Этот ключ запроса уже использован для другой коррекции.")
			}
			result, err = loadCorrection(ctx, tx, orderID, id)
			return err
		}
		if !errors.Is(err, ds.ErrNoRows) {
			return err
		}
		source, err := loadSource(ctx, tx, orderID)
		if err != nil {
			return err
		}
		if source.PaymentID != "" {
			var paymentID string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM public.payments WHERE id=$1 FOR UPDATE`, source.PaymentID).Scan(&paymentID); err != nil {
				return err
			}
		}
		preview, err := prepare(source)
		if err != nil {
			return err
		}
		id = uuid.NewString()
		original, _ := json.Marshal(source.Identity)
		profile, _ := json.Marshal(source.Profile)
		reversal, _ := json.Marshal(preview.Reversal)
		replacement, _ := json.Marshal(preview.Replacement)
		_, err = tx.Exec(ctx, `INSERT INTO public.fiscal_corrections(id,order_id,payment_id,company_id,environment,operator_id,source_version,
			reason,basis,calculation_date,prescription_number,fiscal_snapshot,original_payload,original_identity,reversal_payload,replacement_payload,idempotency_key,request_hash)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::date,$11,$12::jsonb,$13::jsonb,$14::jsonb,$15::jsonb,$16::jsonb,$17,$18)`,
			id, orderID, source.PaymentID, source.CompanyID, source.Environment, operatorID, source.Version, request.Reason, request.Basis, request.CalculationDate,
			request.PrescriptionNumber, string(profile), string(source.RawPayload), string(original), string(reversal), string(replacement), key, hash)
		if err != nil {
			return fmt.Errorf("reserve fiscal correction: %w", err)
		}
		for _, leg := range []struct {
			role    string
			payload []byte
		}{{"reversal", reversal}, {"replacement", replacement}} {
			if err := insertDocument(ctx, tx, id, leg.role, 1, operatorID, "", leg.payload); err != nil {
				return err
			}
		}
		result, err = loadCorrection(ctx, tx, orderID, id)
		return err
	})
	return result, err
}

func insertDocument(ctx context.Context, tx ds.Tx, id, role string, attempt int, operatorID int64, key string, payload []byte) error {
	documentID := uuid.NewString()
	_, err := tx.Exec(ctx, `INSERT INTO public.fiscal_correction_documents(id,correction_id,role,attempt,operator_id,resume_key,request_id,payload)
		VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8::jsonb)`, documentID, id, role, attempt, operatorID, key, "pronogti-correction-"+documentID, string(payload))
	return err
}

func (r *PostgresRepository) Resume(ctx context.Context, orderID, id string, operatorID int64, key string) (Correction, error) {
	conn, release, err := r.connection(ctx)
	if err != nil {
		return Correction{}, err
	}
	defer release()
	var result Correction
	err = ds.WithTx(ctx, conn, func(ctx context.Context, tx ds.Tx) error {
		if err := lockOrder(ctx, tx, orderID); err != nil {
			return err
		}
		current, err := loadCorrection(ctx, tx, orderID, id)
		if err != nil {
			return err
		}
		var previousID string
		err = tx.QueryRow(ctx, `SELECT id::text FROM public.fiscal_correction_documents WHERE correction_id=$1 AND resume_key=$2`, id, key).Scan(&previousID)
		if err == nil {
			result = current
			return nil
		}
		if !errors.Is(err, ds.ErrNoRows) {
			return err
		}
		if current.State != "failed" {
			return conflict("Повторная отправка доступна только после подтверждённой ошибки. Для неопределённого результата выполняется проверка исходного запроса.")
		}
		latest := map[string]Document{}
		for _, d := range current.Documents {
			if d.Attempt > latest[d.Role].Attempt {
				latest[d.Role] = d
			}
		}
		failed := latest["reversal"]
		if failed.State == "issued" || failed.State == "demo" {
			failed = latest["replacement"]
		}
		if failed.State != "failed" {
			return conflict("Нет документа с подтверждённой ошибкой для повторной отправки.")
		}
		if err := insertDocument(ctx, tx, id, failed.Role, failed.Attempt+1, operatorID, key, failed.Payload); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE public.fiscal_corrections SET state='queued',updated_at=now() WHERE id=$1`, id); err != nil {
			return err
		}
		result, err = loadCorrection(ctx, tx, orderID, id)
		return err
	})
	return result, err
}

func (r *PostgresRepository) connection(ctx context.Context) (ds.Conn, func(), error) {
	if r == nil || r.db == nil {
		return nil, nil, errors.New("correction database is not configured")
	}
	lease, server, err := r.db.GetPrimary(ctx)
	if err != nil {
		return nil, nil, err
	}
	return lease.Conn(), func() { r.db.Release(lease, server) }, nil
}

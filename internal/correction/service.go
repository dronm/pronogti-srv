// Package correction records paired fiscal corrections without moving bank funds.
package correction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/dronm/pronogti/internal/fiscal"
	"github.com/dronm/webapp"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var ErrNoJob = errors.New("no correction job available")

type Source struct {
	OrderID           string          `json:"order_id"`
	PaymentID         string          `json:"payment_id"`
	CompanyID         int64           `json:"company_id"`
	Environment       string          `json:"environment"`
	State             string          `json:"state"`
	Version           string          `json:"source_version"`
	CalculationDate   string          `json:"calculation_date"`
	Payload           Receipt         `json:"receipt"`
	Identity          FiscalIdentity  `json:"identity"`
	Profile           fiscal.Profile  `json:"-"`
	RawPayload        json.RawMessage `json:"-"`
	HasRefunds        bool            `json:"-"`
	HasOpenCorrection bool            `json:"-"`
}

type Document struct {
	ID                string          `json:"id"`
	Role              string          `json:"role"`
	Attempt           int             `json:"attempt"`
	RequestID         string          `json:"request_id"`
	State             string          `json:"state"`
	ErrorCode         string          `json:"error_code,omitempty"`
	Response          json.RawMessage `json:"response,omitempty"`
	InitialResponse   json.RawMessage `json:"initial_response,omitempty"`
	Identity          *FiscalIdentity `json:"identity,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	Payload           json.RawMessage `json:"-"`
	DispatchStartedAt *time.Time      `json:"-"`
}

type Correction struct {
	ID                 string         `json:"id"`
	OrderID            string         `json:"order_id"`
	PaymentID          string         `json:"payment_id"`
	CompanyID          int64          `json:"company_id"`
	Environment        string         `json:"environment"`
	OperatorID         int64          `json:"operator_id"`
	SourceVersion      string         `json:"source_version"`
	Reason             string         `json:"reason"`
	Basis              string         `json:"basis"`
	CalculationDate    string         `json:"calculation_date"`
	PrescriptionNumber string         `json:"prescription_number,omitempty"`
	State              string         `json:"state"`
	Original           FiscalIdentity `json:"original"`
	Reversal           Receipt        `json:"reversal"`
	Replacement        Receipt        `json:"replacement"`
	Documents          []Document     `json:"documents"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	Profile            fiscal.Profile `json:"-"`
}

type Quote struct {
	OrderID     string       `json:"order_id"`
	Eligible    bool         `json:"eligible"`
	Reason      string       `json:"reason,omitempty"`
	Source      Source       `json:"source"`
	Corrections []Correction `json:"corrections"`
}

type Options struct {
	AllowLive    bool
	Location     *time.Location
	PollInterval time.Duration
}

type Job struct {
	CorrectionID  string
	Document      Document
	Profile       fiscal.Profile
	Environment   string
	ClaimToken    string
	PreviousState string
}

type Repository interface {
	Load(context.Context, string) (Source, []Correction, error)
	Reserve(context.Context, string, int64, string, string, func(Source) (Preview, error), CreateRequest) (Correction, error)
	Get(context.Context, string, string) (Correction, error)
	List(context.Context) ([]Correction, error)
	Resume(context.Context, string, string, int64, string) (Correction, error)
	Claim(context.Context) (Job, error)
	BeginDispatch(context.Context, Job) error
	Finish(context.Context, Job, string, json.RawMessage, string) error
}

type CorrectionDriver interface {
	CreateCorrection(context.Context, fiscal.DriverConfig, string, json.RawMessage) (fiscal.DriverResult, error)
	LookupCorrection(context.Context, fiscal.DriverConfig, string) (fiscal.DriverResult, error)
}

type Service struct {
	repo    Repository
	driver  CorrectionDriver
	options Options
	now     func() time.Time
}

func NewService(repo Repository, driver CorrectionDriver, options Options) *Service {
	if options.Location == nil {
		options.Location = time.Local
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 2 * time.Second
	}
	return &Service{repo: repo, driver: driver, options: options, now: time.Now}
}

func (s *Service) Quote(ctx context.Context, orderID string) (Quote, error) {
	source, history, err := s.repo.Load(ctx, orderID)
	if err != nil {
		return Quote{}, err
	}
	reason := s.ineligible(source)
	return Quote{OrderID: orderID, Eligible: reason == "", Reason: reason, Source: source, Corrections: history}, nil
}

func (s *Service) Preview(ctx context.Context, orderID string, request CreateRequest) (Preview, error) {
	source, _, err := s.repo.Load(ctx, orderID)
	if err != nil {
		return Preview{}, err
	}
	return s.prepare(source, request)
}

func (s *Service) prepare(source Source, request CreateRequest) (Preview, error) {
	if reason := s.ineligible(source); reason != "" {
		return Preview{}, conflict(reason)
	}
	if request.SourceVersion != source.Version {
		return Preview{}, conflict("Исходный чек изменился. Обновите данные перед коррекцией.")
	}
	preview, err := Build(source, request, s.now(), s.options.Location)
	if err != nil {
		return Preview{}, webapp.BadRequest(err.Error(), nil)
	}
	return preview, nil
}

func (s *Service) Create(ctx context.Context, orderID string, operatorID int64, key string, request CreateRequest) (Correction, error) {
	if operatorID <= 0 || !keyPattern.MatchString(key) {
		return Correction{}, webapp.BadRequest("Требуется корректный ключ запроса коррекции.", nil)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return Correction{}, err
	}
	digest := sha256.Sum256(encoded)
	return s.repo.Reserve(ctx, orderID, operatorID, key, hex.EncodeToString(digest[:]), func(source Source) (Preview, error) { return s.prepare(source, request) }, request)
}

func (s *Service) Get(ctx context.Context, orderID, correctionID string) (Correction, error) {
	return s.repo.Get(ctx, orderID, correctionID)
}
func (s *Service) List(ctx context.Context) ([]Correction, error) { return s.repo.List(ctx) }
func (s *Service) Resume(ctx context.Context, orderID, correctionID string, operatorID int64, key string) (Correction, error) {
	if operatorID <= 0 || !keyPattern.MatchString(key) {
		return Correction{}, webapp.BadRequest("Требуется корректный ключ повторного запроса.", nil)
	}
	current, err := s.repo.Get(ctx, orderID, correctionID)
	if err != nil {
		return Correction{}, err
	}
	if current.Environment == "production" && !s.options.AllowLive {
		return Correction{}, conflict("Рабочая фискализация отключена в настройках сервера.")
	}
	return s.repo.Resume(ctx, orderID, correctionID, operatorID, key)
}

func (s *Service) ineligible(source Source) string {
	if source.HasOpenCorrection {
		return "По этому чеку уже есть незавершённая коррекция. Проверьте её состояние в истории."
	}
	if source.HasRefunds {
		return "По платежу уже создан возврат. Автоматическая коррекция такого чека недоступна."
	}
	if source.State != "issued" && source.State != "demo" {
		return "Исходный чек ещё не выдан или результат его выдачи не определён."
	}
	if source.State == "issued" && (source.Environment != "production" || source.Profile.Mode != "live") {
		return "Режим исходного чека не соответствует рабочей фискализации."
	}
	if source.State == "issued" && !s.options.AllowLive {
		return "Рабочая фискализация отключена в настройках сервера."
	}
	if source.State == "demo" && (source.Environment == "production" || source.Profile.Mode != "demo") {
		return "Для этого платежа нет подтверждённого рабочего чека."
	}
	if source.Payload.CalculationSign != 1 {
		return "Автоматическая коррекция поддерживает только чеки прихода."
	}
	if len(source.RawPayload) == 0 {
		return "Сохранённые данные исходного чека отсутствуют."
	}
	return ""
}

func conflict(message string) error {
	return webapp.Conflict(message, map[string]any{"code": "fiscal_correction_unavailable"})
}

package fiscal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"reflect"

	"github.com/dronm/webapp"
)

const (
	KKTFormat           = "fn-format"
	KKTUnconfirmedCount = "fn-unconfirmed-count"
	KKTShiftStatus      = "shift-status"
	KKTOpenShift        = "open-shift"
	KKTCloseShift       = "close-shift"
	KKTCloseFN          = "close-fn"
	KKTRegisterFN       = "register-fn"
	KKTRetry            = "retry"
)

// Only registration accepts operator-supplied fields. Device, command and
// workflow steps always come from server-side settings and named operations.
type FNRegistration struct {
	ReportType           *uint8  `json:"reportType"`
	UserName             string  `json:"userName"`
	UserInn              string  `json:"userInn"`
	RegistrationNumber   string  `json:"registrationNumber"`
	SettlementAddress    string  `json:"settlementAddress"`
	SettlementPlace      string  `json:"settlementPlace"`
	RepresentativeName   string  `json:"representativeName"`
	RepresentativeInn    string  `json:"representativeInn,omitempty"`
	OfdInn               string  `json:"ofdInn,omitempty"`
	OfdName              string  `json:"ofdName,omitempty"`
	SenderEmail          string  `json:"senderEmail,omitempty"`
	TaxationSystems      uint8   `json:"taxationSystems"`
	ReregistrationReason *uint32 `json:"reregistrationReason,omitempty"`
	MachineNumber        string  `json:"machineNumber,omitempty"`
	ReportAttribute      string  `json:"reportAttribute,omitempty"`
	ReportDataHex        string  `json:"reportDataHex,omitempty"`
	ApplicationFlags     *uint8  `json:"applicationFlags,omitempty"`
	OperatingMode        *uint8  `json:"operatingMode,omitempty"`
}

func (p *FNRegistration) validate() error {
	if p == nil || p.ReportType == nil || *p.ReportType > 1 {
		return webapp.BadRequest("Выберите первоначальную регистрацию или замену ФН.", nil)
	}
	if (*p.ReportType == 0) != (p.ReregistrationReason == nil) || (p.ReregistrationReason != nil && (*p.ReregistrationReason == 0 || *p.ReregistrationReason&^uint32(0x803e7fff) != 0)) {
		return webapp.BadRequest("Проверьте причины изменения сведений при замене ФН.", nil)
	}
	if p.TaxationSystems == 0 || p.TaxationSystems&^uint8(0x37) != 0 {
		return webapp.BadRequest("Выберите системы налогообложения.", nil)
	}
	// goterminalfa validates CP866, tag dependencies, INN and RN checksums
	// against the connected KKT before performing the first mutation.
	return nil
}

type KKTRequest struct {
	Profile
	Action       string          `json:"action"`
	Registration *FNRegistration `json:"registration,omitempty"`
}

type KKTResult struct {
	State          string            `json:"state"`
	Message        string            `json:"message"`
	RequestID      string            `json:"request_id"`
	Data           map[string]uint32 `json:"data,omitempty"`
	DriverResponse json.RawMessage   `json:"driver_response,omitempty"`
}

type KKTProfiles interface {
	ShiftProfile(context.Context, int64) (Profile, error)
	KKTSnapshot(context.Context, string, string, *KKTRequest) (KKTRequest, bool, error)
}

type KKTService struct {
	profiles  KKTProfiles
	allowLive bool
}

func NewKKTService(profiles KKTProfiles, allowLive bool) *KKTService {
	return &KKTService{profiles: profiles, allowLive: allowLive}
}

func isKKTRead(action string) bool {
	return action == KKTFormat || action == KKTUnconfirmedCount || action == KKTShiftStatus
}

func (s *KKTService) Read(ctx context.Context, id int64, action string) (KKTResult, error) {
	if !isKKTRead(action) {
		return KKTResult{}, webapp.BadRequest("Неизвестный запрос состояния ККТ.", nil)
	}
	profile, err := s.profiles.ShiftProfile(ctx, id)
	if err != nil {
		return KKTResult{}, err
	}
	var nonce [16]byte
	rand.Read(nonce[:])
	return s.execute(ctx, KKTRequest{Profile: profile, Action: action}, fmt.Sprintf("pronogti-read-%x", nonce))
}

func (s *KKTService) Run(ctx context.Context, id int64, action, key string, registration *FNRegistration) (KKTResult, error) {
	if id <= 0 || !ValidShiftKey(key) {
		return KKTResult{}, webapp.BadRequest("Некорректный профиль или идентификатор операции.", nil)
	}
	switch action {
	case KKTRegisterFN:
		if err := registration.validate(); err != nil {
			return KKTResult{}, err
		}
	case KKTOpenShift, KKTCloseShift, KKTCloseFN, KKTRetry:
		if registration != nil {
			return KKTResult{}, webapp.BadRequest("Команда не принимает параметры регистрации.", nil)
		}
	default:
		return KKTResult{}, webapp.BadRequest("Неизвестная операция ККТ.", nil)
	}
	scope := fmt.Sprintf("admin-kkt:%d", id)
	requestID := fmt.Sprintf("pronogti-kkt-%x", sha256.Sum256([]byte(scope+":"+key)))
	saved, found, err := s.profiles.KKTSnapshot(ctx, scope, key, nil)
	if err != nil {
		return KKTResult{}, err
	}
	if !found {
		if action == KKTRetry {
			return KKTResult{}, webapp.NotFound("Сохранённый запрос не найден.", nil)
		}
		profile, err := s.profiles.ShiftProfile(ctx, id)
		if err != nil {
			return KKTResult{}, err
		}
		if _, err := prepareKKTProfile(profile, requestID, s.allowLive); err != nil {
			return KKTResult{}, err
		}
		initial := KKTRequest{Profile: profile, Action: action, Registration: registration}
		saved, _, err = s.profiles.KKTSnapshot(ctx, scope, key, &initial)
		if err != nil {
			return KKTResult{}, err
		}
	}
	if action != KKTRetry && (saved.Action != action || !reflect.DeepEqual(saved.Registration, registration)) {
		return KKTResult{}, webapp.Conflict("Идентификатор уже принадлежит другой операции ККТ.", nil)
	}
	return s.execute(ctx, saved, requestID)
}

type kktStep struct {
	Command string         `json:"cmd"`
	Params  map[string]any `json:"params,omitempty"`
}

func (r KKTRequest) command() (command string, params any, finish string) {
	switch r.Action {
	case KKTFormat:
		return "getFiscalStorageFormat", struct{}{}, ""
	case KKTUnconfirmedCount:
		return "getUnconfirmedDocumentCount", struct{}{}, ""
	case KKTShiftStatus:
		return "getShiftParameters", struct{}{}, ""
	case KKTRegisterFN:
		return "registerKkt", r.Registration, ""
	case KKTCloseFN:
		steps := []kktStep{{Command: "beginFiscalStorageClosure"}}
		if r.SettlementAddress != "" && r.SettlementPlace != "" {
			steps = append(steps, kktStep{"sendFiscalStorageClosureData", map[string]any{"documentAddress": r.SettlementAddress, "documentPlace": r.SettlementPlace}})
		}
		finish = "finalizeFiscalStorageClosure"
		steps = append(steps, kktStep{Command: finish})
		return "executeOperation", map[string]any{"commands": steps}, finish
	case KKTOpenShift, KKTCloseShift:
		start, finish := "startOpenShift", "completeOpenShift"
		if r.Action == KKTCloseShift {
			start, finish = "startCloseShift", "completeCloseShift"
		}
		suppressPrint := 0
		if r.SuppressPrint {
			suppressPrint = 1
		}
		cashier := map[string]any{"cashierName": r.CashierName}
		if r.CashierINN != "" {
			cashier["cashierInn"] = r.CashierINN
		}
		steps := []kktStep{{start, map[string]any{"suppressPrint": suppressPrint}}, {"sendCashierData", cashier}, {Command: finish}}
		return "executeOperation", map[string]any{"commands": steps}, finish
	default:
		return "", nil, ""
	}
}

func (s *KKTService) execute(ctx context.Context, request KKTRequest, requestID string) (KKTResult, error) {
	command, params, finish := request.command()
	if command == "" {
		return KKTResult{}, fmt.Errorf("invalid saved KKT operation %q", request.Action)
	}
	cfg, err := prepareKKTProfile(request.Profile, requestID, s.allowLive)
	if err != nil {
		return KKTResult{}, err
	}
	result := KKTResult{RequestID: requestID}
	if request.Mode == "demo" {
		result.State, result.Message = "demo", "Демонстрационный профиль. Команда в ККТ не отправлена."
		return result, nil
	}
	body, err := json.Marshal(params)
	if err != nil {
		return KKTResult{}, err
	}
	out, transportErr := submitDriverCommand(ctx, cfg, requestID, body, command, func(raw json.RawMessage) bool {
		result.Data = kktOutput(request.Action, finish, raw)
		return result.Data != nil
	})
	result.State = out.State
	var reply struct {
		DeviceID  string `json:"deviceId"`
		RequestID string `json:"requestId"`
		Command   string `json:"cmd"`
		Error     *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	detail := ""
	if json.Unmarshal(out.Response, &reply) == nil && reply.DeviceID == cfg.DeviceID && reply.RequestID == requestID && reply.Command == command {
		result.DriverResponse = out.Response
		if reply.Error != nil {
			detail = reply.Error.Code + " " + reply.Error.Message
		}
	}
	switch result.State {
	case "completed":
		result.Message = "Команда выполнена."
	case "failed":
		result.Message = "ККТ отклонила запрос."
	default:
		result.State, result.Message = "unknown", "Результат не подтверждён драйвером."
		if isKKTRead(request.Action) {
			result.State, result.Message = "failed", "Не удалось получить состояние ККТ."
		}
	}
	if detail != "" {
		result.Message += " " + detail
	}
	if transportErr != nil && ctx.Err() != nil {
		result.Message += " Время запроса истекло или соединение прервано."
	}
	return result, nil
}

func kktOutput(action, finish string, raw json.RawMessage) map[string]uint32 {
	if finish != "" {
		var operation struct {
			Results []struct {
				Command string          `json:"cmd"`
				Data    json.RawMessage `json:"data"`
			} `json:"results"`
		}
		if json.Unmarshal(raw, &operation) != nil || len(operation.Results) == 0 {
			return nil
		}
		last := operation.Results[len(operation.Results)-1]
		if last.Command != finish {
			return nil
		}
		raw = last.Data
	}
	var data map[string]*uint32
	if json.Unmarshal(raw, &data) != nil {
		return nil
	}
	fields := []string{"documentNumber", "fiscalSign"}
	switch action {
	case KKTFormat:
		fields = []string{"ffdVersion", "supportedFfdVersion"}
	case KKTUnconfirmedCount:
		count := data["count"]
		if count == nil || *count > math.MaxUint16 {
			return nil
		}
		return map[string]uint32{"count": *count}
	case KKTShiftStatus:
		fields = []string{"shiftState", "shiftNumber", "receiptNumber"}
	case KKTOpenShift, KKTCloseShift:
		fields = append(fields, "shiftNumber")
	}
	values := make(map[string]uint32, len(fields))
	for _, field := range fields {
		if data[field] == nil {
			return nil
		}
		values[field] = *data[field]
	}
	if data["warningFlags"] != nil {
		values["warningFlags"] = *data["warningFlags"]
	}
	return values
}

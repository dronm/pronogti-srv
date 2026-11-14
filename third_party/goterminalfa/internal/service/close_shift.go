package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	terminalfa "github.com/dronm/goterminalfa"
	"github.com/dronm/goterminalfa/internal/protocol"
)

type closeShiftResult struct {
	AlreadyClosed     bool            `json:"alreadyClosed"`
	ShiftNumber       uint64          `json:"shiftNumber"`
	ShiftResult       any             `json:"shiftResult,omitempty"`
	FailedStage       string          `json:"failedStage,omitempty"`
	Failure           *Error          `json:"failure,omitempty"`
	Cleanup           *receiptCleanup `json:"cleanup,omitempty"`
	identity          string
	documentOwned     bool
	mutationAttempted bool
}

type closeShiftError struct {
	Cause   error
	Unknown bool
	Result  *closeShiftResult
}

func (e *closeShiftError) Error() string {
	return fmt.Sprintf("closeShiftIfOpen failed at %s: %v", e.Result.FailedStage, e.Cause)
}

func closeShiftCommand(commands []Command) Command {
	lookup := make(map[byte]Command)
	for _, cmd := range commands {
		if code, ok := operationProtocolCode(cmd.Definition.ProtocolCode); ok {
			lookup[code] = cmd
		}
	}
	prepare := func(raw json.RawMessage) (json.RawMessage, json.RawMessage, error) {
		object, err := operationObject(raw)
		if err != nil {
			return nil, nil, err
		}
		cashier := make(map[string]json.RawMessage)
		start := make(map[string]json.RawMessage)
		for name, value := range object {
			switch name {
			case "cashierName", "cashierInn":
				cashier[name] = value
			case "suppressPrint":
				start[name] = value
			default:
				return nil, nil, fmt.Errorf("unknown closeShiftIfOpen parameter %q", name)
			}
		}
		cashierJSON, _ := json.Marshal(cashier)
		startJSON, _ := json.Marshal(start)
		for _, step := range []struct {
			code byte
			raw  json.RawMessage
		}{{0x20, json.RawMessage(`{}`)}, {0x29, startJSON}, {0x2F, cashierJSON}, {0x2A, json.RawMessage(`{}`)}} {
			if err := lookup[step.code].Validate(step.raw); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", lookup[step.code].Definition.Name, err)
			}
		}
		return startJSON, cashierJSON, nil
	}
	return Command{
		Definition: CommandDefinition{
			Name: "closeShiftIfOpen", Aliases: []string{}, Category: "operations",
			TitleRU:       "Закрыть смену, если она открыта",
			Description:   "Read shift state and close an open shift under one device lock. A closed shift is a successful no-op. One durable requestId covers the entire operation.",
			DescriptionRU: "Проверить состояние смены и закрыть открытую смену с исключительным доступом к устройству. Если смена уже закрыта, команды закрытия не отправляются.",
			ReadOnly:      false, IdempotencyRequired: true, Implemented: true,
			RequestLayout: "application", ResponseLayout: "application",
			Input: append(append([]ParameterDefinition{}, lookup[0x2F].Definition.Input...), registrationSuppressPrint()),
			Output: []ParameterDefinition{
				{Name: "alreadyClosed", JSONType: "boolean", Required: true},
				{Name: "shiftNumber", JSONType: "integer", Required: true},
				{Name: "shiftResult", JSONType: "object", Children: lookup[0x2A].Definition.Output},
			},
			Source: Source{Document: "goterminalfa HTTP API", Version: "0.7.0"},
		},
		Validate: func(raw json.RawMessage) error { _, _, err := prepare(raw); return err },
		Execute: func(ctx context.Context, device *Device, raw json.RawMessage) (any, error) {
			r := &closeShiftResult{identity: device.ExpectedSerialNumber}
			fail := func(cause error) (any, error) {
				return r, &closeShiftError{Cause: cause, Unknown: r.mutationAttempted && !definiteReceiptFailure(cause), Result: r}
			}
			start, cashier, err := prepare(raw)
			if err != nil {
				return fail(&NotExecutedError{Cause: err})
			}
			if r.identity == "" {
				r.FailedStage = "getSerialNumber"
				r.identity, err = device.Client.SerialNumber(ctx)
				if err != nil {
					return fail(err)
				}
			}
			run := func(code byte, params json.RawMessage) (any, error) {
				cmd := lookup[code]
				r.FailedStage = cmd.Definition.Name
				if !cmd.Definition.ReadOnly {
					r.mutationAttempted = true
				}
				return cmd.Execute(ctx, device, params)
			}
			shift, err := run(0x20, json.RawMessage(`{}`))
			if err != nil {
				return fail(err)
			}
			values, ok := shift.(map[string]any)
			if !ok {
				return fail(fmt.Errorf("%w: shift response is not an object", protocol.ErrMalformedFrame))
			}
			state, validState := values["shiftState"].(uint64)
			number, validNumber := values["shiftNumber"].(uint64)
			if !validState || state > 1 || !validNumber {
				return fail(fmt.Errorf("%w: unsupported shift state", protocol.ErrMalformedFrame))
			}
			r.ShiftNumber = number
			if state == 0 {
				r.AlreadyClosed, r.FailedStage = true, ""
				return r, nil
			}
			if _, err = run(0x29, start); err != nil {
				return fail(err)
			}
			r.documentOwned = true
			if _, err = run(0x2F, cashier); err != nil {
				return fail(err)
			}
			r.ShiftResult, err = run(0x2A, json.RawMessage(`{}`))
			if err != nil {
				return fail(err)
			}
			r.documentOwned, r.FailedStage = false, ""
			return r, nil
		},
		OnError: func(ctx context.Context, original *Device, data any, cause error) (any, error) {
			r, ok := data.(*closeShiftResult)
			if !ok {
				return data, cause
			}
			var failure *closeShiftError
			if !errors.As(cause, &failure) {
				failure = &closeShiftError{Cause: cause, Unknown: true, Result: r}
			}
			var primary Response
			_ = json.Unmarshal(executionFailure(Request{}, failure.Cause).Body, &primary)
			r.Failure = primary.Error
			r.Cleanup = &receiptCleanup{}
			if !r.documentOwned {
				r.Cleanup.SkippedReason = "no_confirmed_document_opened_by_request"
				return r, failure
			}
			r.Cleanup.Attempted = true
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
			defer cancel()
			cleanupErr := original.Client.WithSession(cleanupCtx, func(client *terminalfa.Client) error {
				serial, err := client.SerialNumber(cleanupCtx)
				if err != nil {
					return err
				}
				if serial != r.identity {
					return &IdentityError{Expected: r.identity, Actual: serial}
				}
				scoped := *original
				scoped.Client = client
				_, err = lookup[0x10].Execute(cleanupCtx, &scoped, json.RawMessage(`{}`))
				return err
			})
			if cleanupErr != nil {
				failure.Unknown = true
				var response Response
				_ = json.Unmarshal(executionFailure(Request{}, cleanupErr).Body, &response)
				r.Cleanup.Error = response.Error
			} else {
				r.Cleanup.Succeeded = true
			}
			return r, failure
		},
	}
}

func closeShiftFailure(req Request, failure *closeShiftError) Reply {
	if failure.Unknown {
		return JSONReply(409, Response{DeviceID: req.DeviceID, Command: req.Command, RequestID: req.RequestID, Status: "unknown", Data: failure.Result,
			Error: &Error{Code: "outcome_unknown", Message: "shift closing may have executed; reconcile the original request before taking further action; automatic retry is blocked"}})
	}
	standard := executionFailure(req, failure.Cause)
	var response Response
	if json.Unmarshal(standard.Body, &response) != nil {
		return standard
	}
	response.Data = failure.Result
	return JSONReply(standard.HTTPStatus, response)
}

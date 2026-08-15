package history

import (
	"context"
	"encoding/csv"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Vectorking-50kg/s3-rlcd-deck/companion/internal/aisnapshot"
)

var csvHeader = []string{
	"hour_utc",
	"observed_at_utc",
	"provider_id",
	"status",
	"error_code",
	"balance_amount_micros",
	"balance_currency",
	"token_input",
	"token_cached_input",
	"token_output",
	"token_reasoning",
	"token_total",
	"quota_name",
	"used_basis_points",
	"remaining_basis_points",
	"window_minutes",
	"resets_at_utc",
}

// ExportCSV first takes a bounded, independently owned query result and closes
// the SQLite read transaction. A slow download therefore cannot retain a WAL
// snapshot or block the single writer.
func (store *Store) ExportCSV(ctx context.Context, destination io.Writer, query Query) error {
	if destination == nil {
		return ErrInvalid
	}
	records, err := store.Query(ctx, query)
	if err != nil {
		return err
	}
	writer := csv.NewWriter(destination)
	if err = writer.Write(csvHeader); err != nil {
		return err
	}
	for _, record := range records {
		if err = ctx.Err(); err != nil {
			return err
		}
		if len(record.Windows) == 0 {
			if err = writer.Write(csvRecord(record, nil)); err != nil {
				return err
			}
			continue
		}
		for index := range record.Windows {
			if err = writer.Write(csvRecord(record, &record.Windows[index])); err != nil {
				return err
			}
		}
	}
	writer.Flush()
	return writer.Error()
}

func csvRecord(record Record, window *QuotaWindow) []string {
	row := []string{
		record.HourUTC.UTC().Format(time.RFC3339),
		record.ObservedAt.UTC().Format(time.RFC3339Nano),
		csvSafe(record.ProviderID),
		string(record.Status),
		providerErrorText(record.ErrorCode),
		moneyAmountText(record.Balance),
		moneyCurrencyText(record.Balance),
		tokenText(record.Tokens, func(tokens *aisnapshot.TokenUsage) *uint64 { return tokens.Input }),
		tokenText(record.Tokens, func(tokens *aisnapshot.TokenUsage) *uint64 { return tokens.CachedInput }),
		tokenText(record.Tokens, func(tokens *aisnapshot.TokenUsage) *uint64 { return tokens.Output }),
		tokenText(record.Tokens, func(tokens *aisnapshot.TokenUsage) *uint64 { return tokens.Reasoning }),
		tokenText(record.Tokens, func(tokens *aisnapshot.TokenUsage) *uint64 { return tokens.Total }),
		"", "", "", "", "",
	}
	if window != nil {
		row[12] = csvSafe(window.Name)
		row[13] = unsignedText(window.UsedBasisPoints)
		row[14] = unsignedText(window.RemainingBasisPoints)
		row[15] = unsignedText(window.WindowMinutes)
		if window.ResetsAt != nil {
			row[16] = window.ResetsAt.UTC().Format(time.RFC3339Nano)
		}
	}
	return row
}

func csvSafe(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed == "" {
		return value
	}
	switch trimmed[0] {
	case '=', '+', '-', '@':
		return "'" + value
	}
	return value
}

func providerErrorText(value *aisnapshot.ProviderErrorCode) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func moneyAmountText(value *aisnapshot.Money) string {
	if value == nil {
		return ""
	}
	return strconv.FormatUint(value.AmountMicros, 10)
}

func moneyCurrencyText(value *aisnapshot.Money) string {
	if value == nil {
		return ""
	}
	return csvSafe(value.Currency)
}

func tokenText(tokens *aisnapshot.TokenUsage, selectValue func(*aisnapshot.TokenUsage) *uint64) string {
	if tokens == nil {
		return ""
	}
	return unsignedText(selectValue(tokens))
}

func unsignedText[T ~uint16 | ~uint32 | ~uint64](value *T) string {
	if value == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*value), 10)
}

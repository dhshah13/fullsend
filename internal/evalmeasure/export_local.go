package evalmeasure

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	MeasurementsFile = "eval-measurements.jsonl"
	LedgerFile       = "eval-measure-ledger.txt"
)

// AppendMeasurements writes one NDJSON EvaluationResult per line.
func AppendMeasurements(path string, results []EvaluationResult) (retErr error) {
	if len(results) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); retErr == nil {
			retErr = cerr
		}
	}()
	enc := json.NewEncoder(f)
	for _, r := range results {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

func ledgerKey(traceID, evalName, version string) string {
	return traceID + "|" + evalName + "|" + version
}

// AlreadyScored reports whether the ledger contains this measurement.
func AlreadyScored(ledgerPath, traceID, evalName, version string) (bool, error) {
	f, err := os.Open(ledgerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	want := ledgerKey(traceID, evalName, version)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == want {
			return true, nil
		}
	}
	return false, sc.Err()
}

// RecordScored appends a ledger entry.
func RecordScored(ledgerPath, traceID, evalName, version string) (retErr error) {
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(ledgerPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); retErr == nil {
			retErr = cerr
		}
	}()
	_, err = fmt.Fprintln(f, ledgerKey(traceID, evalName, version))
	return err
}

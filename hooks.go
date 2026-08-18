package oascmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// SkipOperation, returned from an OnReadOperation hook, drops the operation
// without treating it as an error.
var SkipOperation = errors.New("oascmd: skip operation")

// Hooks is the extension surface shared by the runtime builder and the
// buildtime generator. All fields are optional.
//
// Which hooks apply where:
//
//	OnReadOperation       runtime + buildtime (filters/transforms the model)
//	OnBeforeCreateCommand runtime + buildtime via gen hooks (see gen docs)
//	OnAfterCreateCommand  runtime only (commands only exist at runtime;
//	                      generated constructors accept per-command tweaks
//	                      at their call site instead)
//	OnBeforeExecute       runtime + generated commands (via ExecOptions)
//	OnAfterExecute        runtime + generated commands (via ExecOptions)
type Hooks struct {
	// OnReadOperation runs for each operation as it is read from the
	// spec. It may mutate op. Returning SkipOperation drops the
	// operation; any other error aborts the build.
	OnReadOperation func(op *Operation) error
	// OnBeforeCreateCommand runs after a command is constructed but
	// before flags are registered on it. It may mutate cmd or veto it by
	// returning SkipOperation.
	OnBeforeCreateCommand func(op Operation, cmd *cobra.Command) error
	// OnAfterCreateCommand runs after the command is fully built (flags
	// registered, RunE set).
	OnAfterCreateCommand func(op Operation, cmd *cobra.Command) error
}

// ApplyRead runs the OnReadOperation hook. The bool reports whether the
// operation should be kept.
func (h Hooks) ApplyRead(op *Operation) (bool, error) {
	if op.Ext.Skip {
		return false, nil
	}
	if h.OnReadOperation == nil {
		return true, nil
	}
	if err := h.OnReadOperation(op); err != nil {
		if errors.Is(err, SkipOperation) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Confirm asks for interactive confirmation on in, reading a y/N answer.
// It backs x-cli-confirm; the --yes flag bypasses it.
func Confirm(in io.Reader, out io.Writer, prompt string) (bool, error) {
	fmt.Fprintf(out, "%s [y/N]: ", prompt)
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

package order_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/migrations"
)

// statuses is every status, as the tests loop over them. TestStatusListIsComplete keeps it in step
// with the constants.
var statuses = []order.Status{
	order.Pending, order.Underpaid, order.Paid, order.Fulfilled, order.Expired, order.Refunded,
}

// allowed lists the nine moves of the state machine. It is written out here, not derived from
// the code under test.
var allowed = map[[2]order.Status]bool{
	{order.Pending, order.Paid}:       true,
	{order.Pending, order.Underpaid}:  true,
	{order.Pending, order.Expired}:    true,
	{order.Underpaid, order.Paid}:     true,
	{order.Underpaid, order.Refunded}: true,
	{order.Paid, order.Fulfilled}:     true,
	{order.Paid, order.Refunded}:      true,
	{order.Fulfilled, order.Refunded}: true,
	{order.Expired, order.Refunded}:   true,
}

func TestTransitionAllPairs(t *testing.T) {
	require.Len(t, allowed, 9)
	pairs := 0
	for _, from := range statuses {
		for _, to := range statuses {
			pairs++
			err := order.Transition(from, to)
			if allowed[[2]order.Status{from, to}] {
				require.NoError(t, err, "%s → %s", from, to)
			} else {
				require.ErrorIs(t, err, order.ErrInvalidTransition, "%s → %s", from, to)
			}
		}
	}
	assert.Equal(t, 36, pairs)
}

func TestTransitionRejectsUnknownStatuses(t *testing.T) {
	unknown := []order.Status{"", "PAID", "Paid", " paid", "paid ", "cancelled"}
	for _, s := range statuses {
		for _, u := range unknown {
			require.ErrorIs(t, order.Transition(s, u), order.ErrInvalidTransition, "%q → %q", s, u)
			require.ErrorIs(t, order.Transition(u, s), order.ErrInvalidTransition, "%q → %q", u, s)
		}
	}
	for _, u := range unknown {
		require.ErrorIs(t, order.Transition(u, u), order.ErrInvalidTransition, "%q → %q", u, u)
	}
}

func TestTransitionErrorNamesBothStatuses(t *testing.T) {
	require.EqualError(t, order.Transition(order.Expired, order.Paid),
		`order: invalid status transition from "expired" to "paid"`)
}

// TestStatusListIsComplete checks statuses against the Status constants declared in the package
// source, so a new status cannot escape the tests that loop over the list.
func TestStatusListIsComplete(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var declared []order.Status
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		require.NoError(t, err)
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				v, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if typ, ok := v.Type.(*ast.Ident); !ok || typ.Name != "Status" {
					continue
				}
				for _, val := range v.Values {
					lit, ok := val.(*ast.BasicLit)
					require.True(t, ok && lit.Kind == token.STRING, "%s: a Status constant that is not a string literal", name)
					s, err := strconv.Unquote(lit.Value)
					require.NoError(t, err)
					declared = append(declared, order.Status(s))
				}
			}
		}
	}
	assert.ElementsMatch(t, statuses, declared)
}

// TestStatusesMatchSchema checks statuses against the newest status CHECK in the migrations, so the
// database accepts every status and nothing else.
func TestStatusesMatchSchema(t *testing.T) {
	files, err := fs.Glob(migrations.FS, "*.up.sql")
	require.NoError(t, err)
	slices.Sort(files)
	check := regexp.MustCompile(`CHECK \(status IN \(([^)]*)\)\)`)
	var values string
	for _, f := range files {
		b, err := fs.ReadFile(migrations.FS, f)
		require.NoError(t, err)
		for _, m := range check.FindAllSubmatch(b, -1) {
			values = string(m[1])
		}
	}
	require.NotEmpty(t, values, "no status CHECK in the migrations")
	var schema []order.Status
	for v := range strings.SplitSeq(values, ",") {
		schema = append(schema, order.Status(strings.Trim(strings.TrimSpace(v), "'")))
	}
	assert.ElementsMatch(t, statuses, schema)
}

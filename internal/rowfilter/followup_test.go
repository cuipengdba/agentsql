package rowfilter

import (
	"strings"
	"testing"
)

func TestCarrySingleTableWhere(t *testing.T) {
	previous := "SELECT region, amount FROM public.orders WHERE region = 'west' LIMIT 2"
	generated := "SELECT pg_catalog.sum(amount) AS total_amount FROM public.orders"
	got, err := CarrySingleTableWhere(previous, generated)
	if err != nil || !strings.Contains(got, "WHERE region = 'west'") || !strings.Contains(got, "pg_catalog.sum(amount)") {
		t.Fatalf("simple prior filter missing: %q %v", got, err)
	}
	got, err = CarrySingleTableWhere(previous, "SELECT amount FROM public.orders WHERE amount > 10")
	if err != nil || !strings.Contains(got, "amount > 10 AND region = 'west'") {
		t.Fatalf("current and prior filters not combined: %q %v", got, err)
	}
}

func TestCarrySingleTableWhereRejectsUnprovenShapes(t *testing.T) {
	cases := []struct{ previous, current string }{
		{"SELECT amount FROM public.orders WHERE region = 'west'", "SELECT amount FROM public.other"},
		{"SELECT amount FROM public.orders WHERE region = 'west'", "SELECT amount FROM public.orders o"},
		{"SELECT amount FROM public.orders WHERE EXISTS (SELECT 1 FROM public.other)", "SELECT amount FROM public.orders"},
		{"SELECT amount FROM public.orders WHERE region = 'west'", "SELECT amount FROM public.orders; DROP TABLE public.orders"},
		{"SELECT amount FROM public.orders WHERE region = 'west'", "SELECT amount FROM public.orders JOIN public.other USING (id)"},
	}
	for _, tc := range cases {
		if sql, err := CarrySingleTableWhere(tc.previous, tc.current); err == nil || sql != "" {
			t.Fatalf("unproven carry accepted: %q %q -> %q %v", tc.previous, tc.current, sql, err)
		}
	}
}

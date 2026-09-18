package mask

import "testing"

import "github.com/stretchr/testify/require"

type maskTestCase struct {
	name     string
	input    string
	expected string
	changed  bool
}

func runMaskTests(t *testing.T, mask func(string) (string, bool), tests []maskTestCase) {
	t.Helper()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, changed := mask(test.input)
			require.Equal(t, test.expected, actual)
			require.Equal(t, test.changed, changed)
		})
	}
}

func TestMaskIDCard(t *testing.T) {
	runMaskTests(t, maskIDCard, []maskTestCase{
		{name: "18 digit uppercase X", input: "11010519491231002X", expected: "110105********002X", changed: true},
		{name: "18 digit lowercase x normalized", input: "11010519491231002x", expected: "110105********002X", changed: true},
		{name: "18 digit numeric checksum", input: "110105194912310021", expected: "110105********0021", changed: true},
		{name: "invalid checksum is not validated", input: "110105194912310020", expected: "110105********0020", changed: true},
		{name: "15 digit legacy", input: "130503670401001", expected: "130503******001", changed: true},
		{name: "surrounding whitespace", input: " \t11010519491231002X\r\n", expected: "110105********002X", changed: true},
		{name: "14 digits fail closed", input: "11010519491231", expected: RedactedFallback, changed: true},
		{name: "19 digits fail closed", input: "1101051949123100211", expected: RedactedFallback, changed: true},
		{name: "letter in body fails closed", input: "1101051949123A002X", expected: RedactedFallback, changed: true},
		{name: "full width digits fail closed", input: "１１０１０５１９４９１２３１００２Ｘ", expected: RedactedFallback, changed: true},
		{name: "empty unchanged", input: "", expected: "", changed: false},
		{name: "NULL sentinel unchanged", input: " NULL ", expected: " NULL ", changed: false},
		{name: "nil sentinel unchanged", input: "<nil>", expected: "<nil>", changed: false},
	})
}

func TestMaskBankCard(t *testing.T) {
	runMaskTests(t, maskBankCard, []maskTestCase{
		{name: "16 digits", input: "4111111111111111", expected: "411111******1111", changed: true},
		{name: "spaces and hyphens", input: "4111 1111-1111 1111", expected: "411111******1111", changed: true},
		{name: "13 digit lower boundary", input: "1234567890123", expected: "123456***0123", changed: true},
		{name: "19 digit upper boundary", input: "1234567890123456789", expected: "123456*********6789", changed: true},
		{name: "Luhn failure is not validated", input: "4111111111111112", expected: "411111******1112", changed: true},
		{name: "consecutive separators", input: "411111--  1111111111", expected: "411111******1111", changed: true},
		{name: "leading zero text", input: "0000000000000", expected: "000000***0000", changed: true},
		{name: "10 digits fail closed", input: "1234567890", expected: RedactedFallback, changed: true},
		{name: "11 digits fail closed", input: "12345678901", expected: RedactedFallback, changed: true},
		{name: "12 digits fail closed", input: "424242424242", expected: RedactedFallback, changed: true},
		{name: "20 digits fail closed", input: "12345678901234567890", expected: RedactedFallback, changed: true},
		{name: "letter fails closed", input: "411111A111111111", expected: RedactedFallback, changed: true},
		{name: "slash fails closed", input: "4111/1111/1111/1111", expected: RedactedFallback, changed: true},
		{name: "Unicode dash fails closed", input: "4111—1111—1111—1111", expected: RedactedFallback, changed: true},
		{name: "full width digits fail closed", input: "４１１１１１１１１１１１１１１１", expected: RedactedFallback, changed: true},
		{name: "only separators fail closed", input: "---", expected: RedactedFallback, changed: true},
		{name: "empty unchanged", input: "", expected: "", changed: false},
		{name: "NULL sentinel unchanged", input: "null", expected: "null", changed: false},
		{name: "nil sentinel unchanged", input: " <NIL> ", expected: " <NIL> ", changed: false},
	})
}

func TestMaskIP(t *testing.T) {
	runMaskTests(t, maskIP, []maskTestCase{
		{name: "IPv4", input: "192.168.1.20", expected: "192.168.*.*", changed: true},
		{name: "IPv4 zero boundary", input: "0.0.0.0", expected: "0.0.*.*", changed: true},
		{name: "IPv4 255 boundary", input: "255.255.255.255", expected: "255.255.*.*", changed: true},
		{name: "PG inet IPv4", input: "192.168.1.20/32", expected: "192.168.*.*", changed: true},
		{name: "CIDR IPv4", input: "192.168.0.0/24", expected: "192.168.*.*", changed: true},
		{name: "zero prefix length", input: "10.20.30.40/0", expected: "10.20.*.*", changed: true},
		{name: "compressed IPv6", input: "2001:db8::1", expected: "2001:0db8:****", changed: true},
		{name: "full uppercase IPv6", input: "2001:0DB8:0000:0000:0000:0000:0000:0001", expected: "2001:0db8:****", changed: true},
		{name: "PG inet IPv6", input: "2001:db8::1/128", expected: "2001:0db8:****", changed: true},
		{name: "IPv6 loopback", input: "::1", expected: "0000:0000:****", changed: true},
		{name: "mapped IPv6 unmapped", input: "::ffff:192.0.2.1", expected: "192.0.*.*", changed: true},
		{name: "surrounding whitespace", input: " 192.168.1.20 ", expected: "192.168.*.*", changed: true},
		{name: "leading zero IPv4 fails closed", input: "192.168.001.20", expected: RedactedFallback, changed: true},
		{name: "zone fails closed", input: "fe80::1%eth0", expected: RedactedFallback, changed: true},
		{name: "host port fails closed", input: "192.168.1.20:443", expected: RedactedFallback, changed: true},
		{name: "hostname fails closed", input: "example.com", expected: RedactedFallback, changed: true},
		{name: "invalid octet fails closed", input: "256.168.1.20", expected: RedactedFallback, changed: true},
		{name: "invalid prefix fails closed", input: "192.168.1.20/33", expected: RedactedFallback, changed: true},
		{name: "empty unchanged", input: "", expected: "", changed: false},
		{name: "NULL sentinel unchanged", input: "NULL", expected: "NULL", changed: false},
		{name: "nil sentinel unchanged", input: "<nil>", expected: "<nil>", changed: false},
	})
}

func TestMaskBirthDate(t *testing.T) {
	runMaskTests(t, maskBirthDate, []maskTestCase{
		{name: "hyphen date", input: "2000-02-29", expected: "2000-**-**", changed: true},
		{name: "slash date", input: "2000/02/29", expected: "2000-**-**", changed: true},
		{name: "dot date", input: "2000.02.29", expected: "2000-**-**", changed: true},
		{name: "compact date", input: "20000229", expected: "2000-**-**", changed: true},
		{name: "RFC3339 UTC", input: "2000-02-29T00:00:00Z", expected: "2000-**-**", changed: true},
		{name: "RFC3339Nano positive offset", input: "2000-02-29T08:09:10.123456789+08:00", expected: "2000-**-**", changed: true},
		{name: "RFC3339Nano negative offset", input: "2000-02-29T08:09:10.1-05:30", expected: "2000-**-**", changed: true},
		{name: "SQL timestamp", input: "2000-02-29 08:09:10", expected: "2000-**-**", changed: true},
		{name: "SQL fractional timestamp", input: "2000-02-29 08:09:10.123456", expected: "2000-**-**", changed: true},
		{name: "future date accepted", input: "2999-12-31", expected: "2999-**-**", changed: true},
		{name: "older than 120 years accepted", input: "1800-01-01", expected: "1800-**-**", changed: true},
		{name: "surrounding whitespace", input: " 2000-02-29 ", expected: "2000-**-**", changed: true},
		{name: "non leap date fails closed", input: "2001-02-29", expected: RedactedFallback, changed: true},
		{name: "month 13 fails closed", input: "2000-13-01", expected: RedactedFallback, changed: true},
		{name: "day 32 fails closed", input: "2000-01-32", expected: RedactedFallback, changed: true},
		{name: "mixed separators fail closed", input: "2000-02/29", expected: RedactedFallback, changed: true},
		{name: "arbitrary suffix fails closed", input: "2000-02-29abc", expected: RedactedFallback, changed: true},
		{name: "empty unchanged", input: "", expected: "", changed: false},
		{name: "NULL sentinel unchanged", input: " NULL ", expected: " NULL ", changed: false},
		{name: "nil sentinel unchanged", input: "<nil>", expected: "<nil>", changed: false},
	})
}

package payments

import "testing"

// Review M3: only an absolute https checkout URL is accepted from the payment company.
func TestReviewM3SecureCheckoutURL(t *testing.T) {
	for s, want := range map[string]bool{
		"https://checkout.example.invalid/pay/1": true,
		"http://checkout.example.invalid/pay/1":  false,
		"javascript:alert(1)":                    false,
		"//checkout.example.invalid/pay":         false,
		"https:///nohost":                        false,
		"https://user:pw@host.invalid/x":         false,
		"":                                       false,
	} {
		if got := secureCheckoutURL(s); got != want {
			t.Errorf("%q: got %v want %v", s, got, want)
		}
	}
}

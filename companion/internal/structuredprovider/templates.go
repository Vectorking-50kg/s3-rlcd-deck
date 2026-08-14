package structuredprovider

const templateSecretReference = "api_key"

// Templates returns owned copies of repository-supported starting points. The
// endpoint/schema values are versioned with AdapterVersion and can be edited as
// a normal structured Provider definition after selection.
func Templates() []Template {
	return []Template{
		{
			ID:          "aihubmix",
			DisplayName: "AIHubMix",
			Definition: Definition{
				ID:          "aihubmix",
				DisplayName: "AIHubMix",
				Request: Request{
					Method:  MethodGET,
					URL:     "https://aihubmix.com/api/user/self",
					Headers: []Header{{Name: "Authorization", Prefix: "Bearer "}},
				},
				Mapping: Mapping{
					BalancePath:    "$.data.quota",
					BalanceDivisor: 500_000,
					FixedCurrency:  "USD",
					WindowName:     "account",
				},
				RefreshMinutes: 5,
			},
			SecretSlots: []string{templateSecretReference},
		},
		{
			ID:          "deepseek",
			DisplayName: "DeepSeek",
			Definition: Definition{
				ID:          "deepseek",
				DisplayName: "DeepSeek",
				Request: Request{
					Method:  MethodGET,
					URL:     "https://api.deepseek.com/user/balance",
					Headers: []Header{{Name: "Authorization", Prefix: "Bearer "}},
				},
				Mapping: Mapping{
					BalancePath:  "$.balance_infos[0].total_balance",
					CurrencyPath: "$.balance_infos[0].currency",
					WindowName:   "account",
				},
				RefreshMinutes: 5,
			},
			SecretSlots: []string{templateSecretReference},
		},
	}
}

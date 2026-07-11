package address

import "encoding/json"

func (a Address) MarshalText() ([]byte, error) {
	return []byte(a.String()), nil
}

func (a *Address) UnmarshalText(raw []byte) error {
	parsed, err := Parse(string(raw))
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

func (a Address) MarshalJSON() ([]byte, error) {
	return json.Marshal(a.String())
}

func (a *Address) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return a.UnmarshalText([]byte(value))
}

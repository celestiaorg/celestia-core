package remote

type Payload struct {
	Status      string        `json:"status"`
	Description string        `json:"description"`
	OrgID       string        `json:"orgID"`
	Permissions []Permissions `json:"permissions"`
}
type Resource struct {
	OrgID string `json:"orgID"`
	Type  string `json:"type"`
}
type Resource0 struct {
	OrgID string `json:"orgID"`
	Type  string `json:"type"`
	Name  string `json:"name"`
}
type Permissions struct {
	Action    string    `json:"action"`
	Resource  Resource  `json:"resource,omitempty"`
	Resource0 Resource0 `json:"resource,omitempty"`
}

// func GetNewToken() (string, error) {
// 	data := Payload{
// 		// fill struct
// 	}
// 	payloadBytes, err := json.Marshal(data)
// 	if err != nil {
// 		// handle err
// 	}
// 	body := bytes.NewReader(payloadBytes)

// 	req, err := http.NewRequest("POST", "http://localhost:8086/api/v2/authorizations", body)
// 	if err != nil {
// 		// handle err
// 	}
// 	req.Header.Set("Authorization", os.ExpandEnv("Token ${INFLUX_TOKEN}"))
// 	req.Header.Set("Content-Type", "application/json")

// 	resp, err := http.DefaultClient.Do(req)
// 	if err != nil {
// 		// handle err
// 	}
// 	defer resp.Body.Close()

// 	resp
// }

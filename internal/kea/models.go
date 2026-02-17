package kea

// Command представляет команду для Kea Control Agent
type Command struct {
	Command string      `json:"command"`
	Service []string    `json:"service"`
	Arguments interface{} `json:"arguments,omitempty"`
}

// Response представляет ответ от Kea Control Agent
type Response struct {
	Result int                    `json:"result"`
	Text   string                 `json:"text"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// Subnet4 представляет подсеть IPv4
type Subnet4 struct {
	ID         int          `json:"id,omitempty"`
	Subnet    string   `json:"subnet"`
	Pools     []Pool   `json:"pools,omitempty"`
	Reservations []Reservation `json:"reservations,omitempty"`
	OptionData []OptionData `json:"option-data,omitempty"`
}

// Pool представляет пул адресов
type Pool struct {
	Pool string `json:"pool"`
}

// Reservation представляет резервацию адреса
type Reservation struct {
	HWAddress string `json:"hw-address"`
	IPAddress string `json:"ip-address"`
	Hostname  string `json:"hostname,omitempty"`
}

// OptionData представляет опцию DHCP
type OptionData struct {
	Name  string      `json:"name"`
	Code  int         `json:"code"`
	Space string      `json:"space"`
	Data  string      `json:"data"`
}

// Subnet4AddArgs аргументы для добавления подсети
type Subnet4AddArgs struct {
	Subnet4 Subnet4 `json:"subnet4"`
}

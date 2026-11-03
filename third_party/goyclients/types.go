package goyclients


type CatalogRef struct {
	ID    int64    `json:"id"`
	Title string `json:"title"`
}

type Account struct {
	ID        int64    `json:"id"`
	Title     string `json:"title"`
	IsCash    bool   `json:"is_cash"`
	IsDefault bool   `json:"is_default"`
}


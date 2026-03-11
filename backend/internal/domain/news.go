package domain

import "time"

type NewsItem struct {
	ID        string
	Headline  string
	Summary   string
	Source    string
	Symbols   []string
	CreatedAt time.Time
	UpdatedAt time.Time
	URL       string
}

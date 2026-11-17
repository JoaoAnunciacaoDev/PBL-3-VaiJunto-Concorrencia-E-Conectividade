package models

import (
	"time"

	"github.com/google/uuid"
)

type RatingType string

const (
	RatingTypePassengerToDriver RatingType = "PASSENGER_TO_DRIVER"
	RatingTypeDriverToPassenger RatingType = "DRIVER_TO_PASSENGER"
	RatingTypeNoShow            RatingType = "NO_SHOW"
	RatingTypeLateCancellation  RatingType = "LATE_CANCELLATION"
)

type Rating struct {
	ID            uuid.UUID  `json:"id"`
	RideID        uuid.UUID  `json:"ride_id"`
	ReservationID uuid.UUID  `json:"reservation_id,omitempty"`
	AuthorID      uuid.UUID  `json:"author_id"`
	AuthorName    string     `json:"author_name,omitempty"`
	TargetID      uuid.UUID  `json:"target_id"`
	TargetName    string     `json:"target_name,omitempty"`
	Type          RatingType `json:"type"`
	Score         int        `json:"score"` // 1 a 5 para avaliações; 0 para no-show/cancelamento
	Comment       string     `json:"comment"`
	CreatedAt     time.Time  `json:"created_at"`
}

type UserReputation struct {
	UserID             uuid.UUID `json:"user_id"`
	UserName           string    `json:"user_name,omitempty"`
	AverageScore       float64   `json:"average_score"`
	TotalRatings       int       `json:"total_ratings"`
	NoShowCount        int       `json:"no_show_count"`
	LateCancelledCount int       `json:"late_cancelled_count"`
	Ratings            []Rating  `json:"ratings"`
}

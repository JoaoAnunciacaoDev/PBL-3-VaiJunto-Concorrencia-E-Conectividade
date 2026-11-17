package protocol

import (
	"github.com/google/uuid"
)

type RateDriverRequest struct {
	ReservationID uuid.UUID `json:"reservation_id"`
	DriverID      uuid.UUID `json:"driver_id"`
	Score         int       `json:"score"`
	Comment       string    `json:"comment"`
}

type RatePassengerRequest struct {
	RideID        uuid.UUID `json:"ride_id"`
	ReservationID uuid.UUID `json:"reservation_id"`
	PassengerID   uuid.UUID `json:"passenger_id"`
	Score         int       `json:"score"`
	Comment       string    `json:"comment"`
}

type GetUserReputationRequest struct {
	UserID uuid.UUID `json:"user_id"`
}

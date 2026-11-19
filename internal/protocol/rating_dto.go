package protocol

import (
	"github.com/google/uuid"
)

type RateDriverRequest struct {
	ReservationID uuid.UUID `json:"reservation_id"`
	RideID        uuid.UUID `json:"ride_id,omitempty"`
	SegmentID     uuid.UUID `json:"segment_id,omitempty"`
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

type FileComplaintRequest struct {
	RideID        uuid.UUID `json:"ride_id,omitempty"`
	ReservationID uuid.UUID `json:"reservation_id,omitempty"`
	TargetID      uuid.UUID `json:"target_id,omitempty"`
	Comment       string    `json:"comment"`
}

type GetUserReputationRequest struct {
	UserID uuid.UUID `json:"user_id"`
}

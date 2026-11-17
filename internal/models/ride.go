package models

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type RideStatus string

const (
	RideStatusScheduled       RideStatus = "SCHEDULED"
	RideStatusInProgress      RideStatus = "IN_PROGRESS"
	RideStatusCompleted       RideStatus = "COMPLETED"
	RideStatusCompletedNoShow RideStatus = "COMPLETED_NO_SHOW"
	RideStatusCancelled       RideStatus = "CANCELLED"
)

type Ride struct {
	ID          uuid.UUID  `json:"id"`
	DriverID    uuid.UUID  `json:"driver_id"`
	DriverName  string     `json:"driver_name,omitempty"`
	CompanyID   string     `json:"company_id,omitempty"`
	DepartureAt time.Time  `json:"departure_at"`
	Segments    []Stage    `json:"segments"`
	Cancelled   bool       `json:"cancelled"`
	Status      RideStatus `json:"status"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

func (r *Ride) GetStatus() RideStatus {
	if r.Status != "" {
		return r.Status
	}
	if r.Cancelled {
		return RideStatusCancelled
	}
	return RideStatusScheduled
}

func (r *Ride) Start(now time.Time) error {
	if r.Cancelled || r.GetStatus() != RideStatusScheduled {
		return errors.New("apenas caronas agendadas e não canceladas podem ser iniciadas")
	}
	r.Status = RideStatusInProgress
	start := now
	r.StartedAt = &start
	return nil
}

func (r *Ride) Finish(now time.Time) error {
	if r.Cancelled || r.GetStatus() == RideStatusCancelled {
		return errors.New("não é possível concluir uma carona cancelada")
	}
	if r.GetStatus() != RideStatusInProgress {
		return errors.New("apenas caronas em andamento podem ser concluídas")
	}

	for i := range r.Segments {
		r.Segments[i].Status = StageStatusCompleted
		done := now
		r.Segments[i].CompletedAt = &done
	}

	r.Status = RideStatusCompleted
	done := now
	r.CompletedAt = &done
	return nil
}

// FinishSegment conclui um trecho específico da carona.
// Se todos os trechos forem concluídos, a carona como um todo passa para COMPLETED.
func (r *Ride) FinishSegment(segmentID uuid.UUID, now time.Time) error {
	if r.Cancelled || r.GetStatus() == RideStatusCancelled {
		return errors.New("não é possível concluir trecho de uma carona cancelada")
	}
	if r.GetStatus() == RideStatusCompleted {
		return errors.New("a carona inteira já foi concluída")
	}

	found := false
	for i := range r.Segments {
		if r.Segments[i].ID == segmentID {
			if r.Segments[i].GetStatus() == StageStatusCompleted {
				return errors.New("este trecho já foi concluído anteriormente")
			}
			r.Segments[i].Status = StageStatusCompleted
			done := now
			r.Segments[i].CompletedAt = &done
			found = true
			break
		}
	}

	if !found {
		return errors.New("trecho não encontrado nesta carona")
	}

	if r.GetStatus() == RideStatusScheduled {
		r.Status = RideStatusInProgress
		start := now
		r.StartedAt = &start
	}

	allCompleted := true
	for _, s := range r.Segments {
		if s.GetStatus() != StageStatusCompleted {
			allCompleted = false
			break
		}
	}

	if allCompleted {
		r.Status = RideStatusCompleted
		done := now
		r.CompletedAt = &done
	}

	return nil
}

// IsSegmentCompleted verifica se um trecho específico foi finalizado, ou se toda a carona já foi concluída.
func (r *Ride) IsSegmentCompleted(segmentID uuid.UUID) bool {
	if r.GetStatus() == RideStatusCompleted || r.GetStatus() == RideStatusCompletedNoShow {
		return true
	}
	for _, s := range r.Segments {
		if s.ID == segmentID && s.GetStatus() == StageStatusCompleted {
			return true
		}
	}
	return false
}

func (r *Ride) ReportNoShow(now time.Time) error {
	status := r.GetStatus()
	if status != RideStatusInProgress && status != RideStatusScheduled {
		return errors.New("não é possível reportar ausência para uma carona que não esteja agendada ou em andamento")
	}
	r.Status = RideStatusCompletedNoShow
	done := now
	r.CompletedAt = &done
	return nil
}

func (r *Ride) IsValid() bool {
	if r.ID == uuid.Nil || r.DriverID == uuid.Nil || r.DepartureAt.IsZero() || len(r.Segments) == 0 {
		return false
	}
	if !r.DepartureAt.Equal(r.Segments[0].DepartureAt) {
		return false
	}

	return areSegmentsContinuous(r.Segments)
}

func (r *Ride) Cancel() {
	r.Cancelled = true
	r.Status = RideStatusCancelled
}

func (r *Ride) AddSegment(segment Stage) bool {
	if !segment.IsValid() {
		return false
	}

	if len(r.Segments) > 0 {
		lastSegment := r.Segments[len(r.Segments)-1]
		if lastSegment.Destination != segment.Origin || lastSegment.ArrivalAt.After(segment.DepartureAt) {
			return false
		}
	}

	r.Segments = append(r.Segments, segment)
	return true
}

func (r *Ride) RemoveSegment(index int) bool {
	if index < 0 || index >= len(r.Segments) {
		return false
	}

	updatedSegments := append([]Stage(nil), r.Segments[:index]...)
	updatedSegments = append(updatedSegments, r.Segments[index+1:]...)

	if len(updatedSegments) == 0 || !areSegmentsContinuous(updatedSegments) {
		return false
	}

	r.Segments = updatedSegments
	return true
}

func (r *Ride) UpdateSegment(index int, segment Stage) bool {
	if index < 0 || index >= len(r.Segments) || !segment.IsValid() {
		return false
	}

	updatedSegments := append([]Stage(nil), r.Segments...)
	updatedSegments[index] = segment
	if !areSegmentsContinuous(updatedSegments) {
		return false
	}

	r.Segments = updatedSegments
	return true
}

func (r *Ride) TotalPriceCents() int {
	totalPrice := 0

	for _, segment := range r.Segments {
		totalPrice += segment.PriceCents
	}

	return totalPrice
}

func (r *Ride) MinimumAvailableSeats() int {
	if r.Cancelled || len(r.Segments) == 0 {
		return 0
	}

	minimumAvailableSeats := r.Segments[0].AvailableSeats
	for _, segment := range r.Segments[1:] {
		if segment.AvailableSeats < minimumAvailableSeats {
			minimumAvailableSeats = segment.AvailableSeats
		}
	}

	return minimumAvailableSeats
}

func areSegmentsContinuous(segments []Stage) bool {
	for index, segment := range segments {
		if !segment.IsValid() {
			return false
		}

		if index > 0 && (segments[index-1].Destination != segment.Origin || segments[index-1].ArrivalAt.After(segment.DepartureAt)) {
			return false
		}
	}

	return true
}

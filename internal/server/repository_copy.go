package server

import "github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"

func cloneUser(user *models.User) *models.User {
	if user == nil {
		return nil
	}
	copy := *user
	return &copy
}

func cloneDriver(driver *models.Driver) *models.Driver {
	if driver == nil {
		return nil
	}
	copy := *driver
	if driver.Vehicle != nil {
		vehicleCopy := *driver.Vehicle
		copy.Vehicle = &vehicleCopy
	}
	return &copy
}

func cloneRide(ride *models.Ride) *models.Ride {
	if ride == nil {
		return nil
	}
	copy := *ride
	copy.Segments = make([]models.Stage, len(ride.Segments))
	for i, seg := range ride.Segments {
		if seg.CompletedAt != nil {
			ct := *seg.CompletedAt
			seg.CompletedAt = &ct
		}
		copy.Segments[i] = seg
	}
	if ride.StartedAt != nil {
		st := *ride.StartedAt
		copy.StartedAt = &st
	}
	if ride.CompletedAt != nil {
		ct := *ride.CompletedAt
		copy.CompletedAt = &ct
	}
	return &copy
}

func cloneSegmentBooking(booking *models.SegmentBooking) *models.SegmentBooking {
	if booking == nil {
		return nil
	}
	copy := *booking
	return &copy
}

func cloneRating(rating *models.Rating) *models.Rating {
	if rating == nil {
		return nil
	}
	copy := *rating
	return &copy
}

func cloneReservation(reservation *models.Reservation) *models.Reservation {
	if reservation == nil {
		return nil
	}
	copy := *reservation
	copy.Segments = append([]models.ReservedSegment(nil), reservation.Segments...)
	return &copy
}

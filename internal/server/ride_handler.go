package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/protocol"
	"github.com/google/uuid"
)

func (s *Server) handleCreateRide(payload json.RawMessage, session *Session) protocol.Response {
	driver, response, ok := s.driverForSession(session, true)
	if !ok {
		return response
	}

	var dto protocol.CreateRideRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil {
		return protocol.Response{Success: "error", Message: "Requisição inválida: " + err.Error()}
	}

	if dto.DepartureAt.IsZero() || len(dto.Segments) == 0 {
		return protocol.Response{Success: "error", Message: "Data de partida e ao menos um trecho são obrigatórios."}
	}

	segments := make([]models.Stage, 0, len(dto.Segments))
	for index, requestedSegment := range dto.Segments {
		segment := models.Stage{
			ID:             uuid.New(),
			Origin:         requestedSegment.Origin,
			Destination:    requestedSegment.Destination,
			DepartureAt:    requestedSegment.DepartureAt,
			ArrivalAt:      requestedSegment.ArrivalAt,
			PriceCents:     requestedSegment.PriceCents,
			AvailableSeats: requestedSegment.AvailableSeats,
		}

		if !segment.IsValid() || segment.AvailableSeats == 0 {
			return protocol.Response{Success: "error", Message: fmt.Sprintf("Trecho %d é inválido.", index+1)}
		}
		if segment.AvailableSeats > driver.Vehicle.SeatCapacity {
			return protocol.Response{Success: "error", Message: fmt.Sprintf("Trecho %d excede a capacidade do veículo.", index+1)}
		}

		segments = append(segments, segment)
	}

	ride := &models.Ride{
		ID:          uuid.New(),
		DriverID:    driver.ID,
		DriverName:  driver.Name,
		CompanyID:   s.companyID,
		DepartureAt: dto.DepartureAt,
		Segments:    segments,
		Status:      models.RideStatusScheduled,
	}
	if !ride.IsValid() {
		return protocol.Response{Success: "error", Message: "A rota da carona deve possuir trechos contínuos, em ordem de horário e válidos."}
	}

	if err := s.repository.SaveRideForDriver(ride); err != nil {
		return protocol.Response{Success: "error", Message: err.Error()}
	}

	return rideResponse(ride, "Carona publicada com sucesso.")
}

func (s *Server) handleListMyRides(session *Session) protocol.Response {
	driver, response, ok := s.driverForSession(session, false)
	if !ok {
		return response
	}

	rides, err := s.repository.GetRidesByDriverID(driver.ID)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Não foi possível consultar as caronas."}
	}

	return rideResponse(rides, "Caronas obtidas com sucesso.")
}

func (s *Server) handleCancelRide(payload json.RawMessage, session *Session) protocol.Response {
	driver, response, ok := s.driverForSession(session, false)
	if !ok {
		return response
	}

	var dto protocol.CancelRideRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil || dto.RideID == uuid.Nil {
		return protocol.Response{Success: "error", Message: "Identificador da carona inválido."}
	}

	cancelRes, err := s.repository.CancelRideDetailed(dto.RideID, driver.ID, s.companyID)
	if err != nil {
		return protocol.Response{Success: "error", Message: err.Error()}
	}

	if cancelRes.HadPassengers {
		cancelRecord := &models.Rating{
			ID:         uuid.New(),
			RideID:     dto.RideID,
			AuthorID:   driver.ID,
			AuthorName: driver.Name,
			TargetID:   driver.ID,
			TargetName: driver.Name,
			Type:       models.RatingTypeDriverCancellation,
			Score:      0,
			Comment:    "Motorista cancelou carona que já possuía passageiros confirmados.",
			CreatedAt:  time.Now(),
		}
		if err := s.repository.SaveRating(cancelRecord); err != nil {
			log.Printf("[cancel_ride] falha ao registrar cancelamento do motorista: %v", err)
		} else {
			s.replicateRatingToPeers(cancelRecord)
		}
	}

	for _, rel := range cancelRes.RemoteSegmentsToRelease {
		s.releaseRemoteSegmentWithRetry(rel.CompanyID, rel.RideID, rel.SegmentID, rel.ReservationID, rel.PassengerID)
	}

	notifiedReservations := make(map[uuid.UUID]bool)
	for _, booking := range cancelRes.RemoteBookings {
		if booking.ReservationID == uuid.Nil || booking.CompanyID == "" || booking.CompanyID == s.companyID {
			continue
		}
		if notifiedReservations[booking.ReservationID] {
			continue
		}
		notifiedReservations[booking.ReservationID] = true
		s.notifyRemoteRideCancelledWithRetry(booking.CompanyID, booking.ReservationID, dto.RideID)
	}

	return protocol.Response{Success: "success", Message: "Carona cancelada com sucesso."}
}

func (s *Server) notifyRemoteRideCancelledWithRetry(companyID string, reservationID, rideID uuid.UUID) {
	if s.peers == nil {
		return
	}
	peer, exists := s.peers.Get(companyID)
	if !exists {
		return
	}

	body, _ := json.Marshal(map[string]any{
		"cancelled_company_id": s.companyID,
		"ride_id":              rideID,
	})
	url := fmt.Sprintf("%s/internal/v1/reservations/%s/ride-cancelled", peer.InternalURL, reservationID)

	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if attempt < 3 {
			time.Sleep(150 * time.Millisecond)
		}
	}
}

func (s *Server) handleStartRide(payload json.RawMessage, session *Session) protocol.Response {
	driver, response, ok := s.driverForSession(session, false)
	if !ok {
		return response
	}

	var dto protocol.StartRideRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil || dto.RideID == uuid.Nil {
		return protocol.Response{Success: "error", Message: "Identificador da carona inválido."}
	}

	ride, err := s.repository.StartRide(dto.RideID, driver.ID)
	if err != nil {
		return protocol.Response{Success: "error", Message: err.Error()}
	}

	return rideResponse(ride, "Carona iniciada com sucesso.")
}

func (s *Server) handleFinishRide(payload json.RawMessage, session *Session) protocol.Response {
	driver, response, ok := s.driverForSession(session, false)
	if !ok {
		return response
	}

	var dto protocol.FinishRideRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil || dto.RideID == uuid.Nil {
		return protocol.Response{Success: "error", Message: "Identificador da carona inválido."}
	}

	ride, err := s.repository.FinishRide(dto.RideID, driver.ID)
	if err != nil {
		return protocol.Response{Success: "error", Message: err.Error()}
	}

	return rideResponse(ride, "Carona finalizada com sucesso.")
}

func (s *Server) handleFinishSegment(payload json.RawMessage, session *Session) protocol.Response {
	driver, response, ok := s.driverForSession(session, false)
	if !ok {
		return response
	}

	var dto protocol.FinishSegmentRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil || dto.RideID == uuid.Nil || dto.SegmentID == uuid.Nil {
		return protocol.Response{Success: "error", Message: "Identificadores de carona e trecho são obrigatórios."}
	}

	ride, err := s.repository.FinishSegment(dto.RideID, dto.SegmentID, driver.ID)
	if err != nil {
		return protocol.Response{Success: "error", Message: err.Error()}
	}

	msg := "Trecho concluído com sucesso."
	if ride.GetStatus() == models.RideStatusCompleted {
		msg = "Trecho concluído com sucesso. Todos os trechos foram finalizados e a carona foi concluída!"
	}

	return rideResponse(ride, msg)
}

func (s *Server) handleReportNoShow(payload json.RawMessage, session *Session) protocol.Response {
	driver, response, ok := s.driverForSession(session, false)
	if !ok {
		return response
	}

	var dto protocol.ReportNoShowRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil || dto.RideID == uuid.Nil || dto.PassengerID == uuid.Nil {
		return protocol.Response{Success: "error", Message: "Identificadores de carona e passageiro são obrigatórios."}
	}

	ride, err := s.repository.GetRideByID(dto.RideID)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Carona não encontrada."}
	}
	if ride.DriverID != driver.ID {
		return protocol.Response{Success: "error", Message: "A carona não pertence ao motorista autenticado."}
	}
	rideStatus := ride.GetStatus()
	if ride.Cancelled || (rideStatus != models.RideStatusScheduled && rideStatus != models.RideStatusInProgress) {
		return protocol.Response{Success: "error", Message: "Só é possível reportar ausência em caronas agendadas ou em andamento."}
	}

	resID, _, _, found := s.repository.HasConfirmedBookingForRide(dto.RideID, dto.PassengerID)
	if !found {
		return protocol.Response{Success: "error", Message: "Passageiro não possui reserva confirmada nesta carona."}
	}
	if dto.ReservationID == uuid.Nil {
		dto.ReservationID = resID
	}

	if s.repository.HasRatingOfType(driver.ID, dto.RideID, dto.PassengerID, models.RatingTypeNoShow) ||
		s.repository.HasNoShowForRide(dto.RideID, dto.PassengerID) {
		return protocol.Response{Success: "error", Message: "Ausência (No-show) já foi registrada para este passageiro nesta carona."}
	}

	if _, err := s.repository.MarkBookingNoShow(dto.RideID, dto.PassengerID); err != nil {
		return protocol.Response{Success: "error", Message: err.Error()}
	}

	targetName := s.resolvePassengerNameForRide(dto.RideID, dto.PassengerID)

	noShowRecord := &models.Rating{
		ID:            uuid.New(),
		RideID:        dto.RideID,
		ReservationID: dto.ReservationID,
		AuthorID:      driver.ID,
		AuthorName:    driver.Name,
		TargetID:      dto.PassengerID,
		TargetName:    targetName,
		Type:          models.RatingTypeNoShow,
		Score:         0,
		Comment:       dto.Comment,
		CreatedAt:     time.Now(),
	}
	if noShowRecord.Comment == "" {
		noShowRecord.Comment = "Passageiro não compareceu ao local de embarque (No-show)."
	}

	if err := s.repository.SaveRating(noShowRecord); err != nil {
		return protocol.Response{Success: "error", Message: "Erro ao registrar ocorrência: " + err.Error()}
	}

	s.replicateRatingToPeers(noShowRecord)

	return protocol.Response{Success: "success", Message: "Ausência de passageiro (No-show) registrada com sucesso."}
}

func (s *Server) resolvePassengerNameForRide(rideID, passengerID uuid.UUID) string {
	if u, err := s.repository.GetUserByID(passengerID); err == nil && u.Name != "" {
		return u.Name
	}
	if passengersBySeg, err := s.repository.GetConfirmedPassengersForRide(rideID); err == nil {
		for _, bookings := range passengersBySeg {
			for _, b := range bookings {
				if b.PassengerID == passengerID && b.PassengerName != "" {
					return b.PassengerName
				}
			}
		}
	}
	return ""
}

func rideResponse(data any, message string) protocol.Response {
	payload, err := json.Marshal(data)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Não foi possível processar a resposta."}
	}

	return protocol.Response{Success: "success", Message: message, Payload: payload}
}

package server

import (
	"encoding/json"
	"fmt"

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

	if err := s.repository.CancelRide(dto.RideID, driver.ID); err != nil {
		return protocol.Response{Success: "error", Message: err.Error()}
	}

	return protocol.Response{Success: "success", Message: "Carona cancelada com sucesso."}
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

	_, _ = s.repository.SetRideNoShow(dto.RideID, driver.ID)

	targetUser, _ := s.repository.GetUserByID(dto.PassengerID)
	targetName := ""
	if targetUser != nil {
		targetName = targetUser.Name
	}

	noShowRecord := &models.Rating{
		RideID:        dto.RideID,
		ReservationID: dto.ReservationID,
		AuthorID:      driver.ID,
		AuthorName:    driver.Name,
		TargetID:      dto.PassengerID,
		TargetName:    targetName,
		Type:          models.RatingTypeNoShow,
		Score:         0,
		Comment:       dto.Comment,
	}
	if noShowRecord.Comment == "" {
		noShowRecord.Comment = "Passageiro não compareceu ao local de embarque (No-show)."
	}

	if err := s.repository.SaveRating(noShowRecord); err != nil {
		return protocol.Response{Success: "error", Message: "Erro ao registrar ocorrência: " + err.Error()}
	}

	return protocol.Response{Success: "success", Message: "Ausência de passageiro (No-show) registrada com sucesso."}
}

func rideResponse(data any, message string) protocol.Response {
	payload, err := json.Marshal(data)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Não foi possível processar a resposta."}
	}

	return protocol.Response{Success: "success", Message: message, Payload: payload}
}

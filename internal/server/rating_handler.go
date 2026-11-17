package server

import (
	"encoding/json"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/enum"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/protocol"
	"github.com/google/uuid"
)

func (s *Server) handleRateDriver(payload json.RawMessage, session *Session) protocol.Response {
	if response, ok := s.passengerForSession(session); !ok {
		return response
	}

	var dto protocol.RateDriverRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil {
		return protocol.Response{Success: "error", Message: "Requisição inválida: " + err.Error()}
	}

	if dto.Score < 1 || dto.Score > 5 {
		return protocol.Response{Success: "error", Message: "A nota da avaliação deve estar entre 1 e 5."}
	}
	if dto.ReservationID == uuid.Nil {
		return protocol.Response{Success: "error", Message: "Identificador da reserva é obrigatório para comprovar a viagem."}
	}

	reservation, err := s.repository.GetReservationByID(dto.ReservationID)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Reserva não encontrada."}
	}
	if reservation.PassengerID != session.UserID {
		return protocol.Response{Success: "error", Message: "A reserva informada não pertence ao passageiro autenticado."}
	}
	if reservation.Status != enum.Confirmada {
		return protocol.Response{Success: "error", Message: "Apenas reservas confirmadas podem ser avaliadas."}
	}

	// Localiza o trecho e a carona correspondente ao motorista
	var targetRide *models.Ride
	var targetDriverID uuid.UUID

	for _, seg := range reservation.Segments {
		ride, rErr := s.repository.GetRideByID(seg.RideID)
		if rErr == nil {
			if dto.DriverID == uuid.Nil || ride.DriverID == dto.DriverID {
				targetRide = ride
				targetDriverID = ride.DriverID
				break
			}
		}
	}

	if targetRide == nil {
		return protocol.Response{Success: "error", Message: "Carona correspondente à reserva não encontrada neste nó."}
	}

	// Regra fundamental: o passageiro só pode avaliar uma viagem cujo trecho ou carona foi concluída!
	segmentCompleted := false
	for _, seg := range reservation.Segments {
		if seg.RideID == targetRide.ID {
			if targetRide.IsSegmentCompleted(seg.SegmentID) {
				segmentCompleted = true
				break
			}
		}
	}

	if !segmentCompleted {
		return protocol.Response{Success: "error", Message: "O trecho da carona ainda não foi concluído pelo motorista."}
	}

	if s.repository.HasUserRated(session.UserID, targetRide.ID, targetDriverID) {
		return protocol.Response{Success: "error", Message: "Você já avaliou este motorista para esta carona."}
	}

	driverUser, _ := s.repository.GetUserByID(targetDriverID)
	driverName := targetRide.DriverName
	if driverUser != nil && driverName == "" {
		driverName = driverUser.Name
	}

	rating := &models.Rating{
		RideID:        targetRide.ID,
		ReservationID: reservation.ID,
		AuthorID:      session.UserID,
		AuthorName:    session.UserName,
		TargetID:      targetDriverID,
		TargetName:    driverName,
		Type:          models.RatingTypePassengerToDriver,
		Score:         dto.Score,
		Comment:       dto.Comment,
	}

	if err := s.repository.SaveRating(rating); err != nil {
		return protocol.Response{Success: "error", Message: "Falha ao registrar avaliação: " + err.Error()}
	}

	return reputationResponse(rating, "Avaliação do motorista registrada com sucesso.")
}

func (s *Server) handleRatePassenger(payload json.RawMessage, session *Session) protocol.Response {
	driver, response, ok := s.driverForSession(session, false)
	if !ok {
		return response
	}

	var dto protocol.RatePassengerRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil {
		return protocol.Response{Success: "error", Message: "Requisição inválida: " + err.Error()}
	}

	if dto.Score < 1 || dto.Score > 5 {
		return protocol.Response{Success: "error", Message: "A nota da avaliação deve estar entre 1 e 5."}
	}
	if dto.RideID == uuid.Nil || dto.PassengerID == uuid.Nil {
		return protocol.Response{Success: "error", Message: "Identificadores da carona e do passageiro são obrigatórios."}
	}

	ride, err := s.repository.GetRideByID(dto.RideID)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Carona não encontrada."}
	}
	if ride.DriverID != driver.ID {
		return protocol.Response{Success: "error", Message: "A carona não pertence ao motorista autenticado."}
	}

	if ride.GetStatus() != models.RideStatusCompleted && ride.GetStatus() != models.RideStatusCompletedNoShow {
		return protocol.Response{Success: "error", Message: "Apenas caronas finalizadas podem ter passageiros avaliados."}
	}

	if s.repository.HasUserRated(driver.ID, dto.RideID, dto.PassengerID) {
		return protocol.Response{Success: "error", Message: "Você já avaliou este passageiro para esta carona."}
	}

	targetUser, _ := s.repository.GetUserByID(dto.PassengerID)
	targetName := ""
	if targetUser != nil {
		targetName = targetUser.Name
	}

	rating := &models.Rating{
		RideID:        dto.RideID,
		ReservationID: dto.ReservationID,
		AuthorID:      driver.ID,
		AuthorName:    driver.Name,
		TargetID:      dto.PassengerID,
		TargetName:    targetName,
		Type:          models.RatingTypeDriverToPassenger,
		Score:         dto.Score,
		Comment:       dto.Comment,
	}

	if err := s.repository.SaveRating(rating); err != nil {
		return protocol.Response{Success: "error", Message: "Falha ao registrar avaliação: " + err.Error()}
	}

	return reputationResponse(rating, "Avaliação do passageiro registrada com sucesso.")
}

func (s *Server) handleGetUserReputation(payload json.RawMessage, session *Session) protocol.Response {
	var dto protocol.GetUserReputationRequest
	if len(payload) > 0 && string(payload) != "null" {
		_ = protocol.UnmarshalStrict(payload, &dto)
	}

	targetUserID := dto.UserID
	if targetUserID == uuid.Nil {
		if session != nil && session.IsAuthenticated() {
			targetUserID = session.UserID
		} else {
			return protocol.Response{Success: "error", Message: "Identificador de usuário não informado."}
		}
	}

	reputation, err := s.repository.GetReputationByUserID(targetUserID)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Não foi possível obter a reputação: " + err.Error()}
	}

	return reputationResponse(reputation, "Reputação obtida com sucesso.")
}

func reputationResponse(data any, message string) protocol.Response {
	payload, err := json.Marshal(data)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Não foi possível processar a resposta."}
	}

	return protocol.Response{Success: "success", Message: message, Payload: payload}
}

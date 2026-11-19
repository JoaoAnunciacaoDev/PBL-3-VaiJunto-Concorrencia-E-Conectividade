package server

import (
	"encoding/json"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/protocol"
	"github.com/google/uuid"
)

func (s *Server) handleGetRidePassengers(payload json.RawMessage, session *Session) protocol.Response {
	driver, response, ok := s.driverForSession(session, false)
	if !ok {
		return response
	}

	var dto protocol.GetRidePassengersRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil || dto.RideID == uuid.Nil {
		return protocol.Response{Success: "error", Message: "Identificador da carona inválido."}
	}

	ride, err := s.repository.GetRideByID(dto.RideID)
	if err != nil {
		return protocol.Response{Success: "error", Message: err.Error()}
	}
	if ride.DriverID != driver.ID {
		return protocol.Response{Success: "error", Message: "Motorista não pode consultar passageiros desta carona."}
	}

	passengersBySegment, err := s.repository.GetConfirmedPassengersForRide(ride.ID)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Não foi possível consultar os passageiros."}
	}

	segments := make([]protocol.RideSegmentPassengersResponse, 0, len(ride.Segments))
	for _, stage := range ride.Segments {
		bookings := passengersBySegment[stage.ID]
		passengers := make([]protocol.UserResponse, 0, len(bookings))
		for _, booking := range bookings {
			name := booking.PassengerName
			email := booking.PassengerEmail
			if name == "" || email == "" {
				if u, uErr := s.repository.GetUserByID(booking.PassengerID); uErr == nil {
					if name == "" {
						name = u.Name
					}
					if email == "" {
						email = u.Email
					}
				}
			}
			companyID := booking.CompanyID
			if companyID == "" {
				companyID = s.companyID
			}
			passengers = append(passengers, protocol.UserResponse{
				ID:        booking.PassengerID,
				Name:      name,
				Email:     email,
				Role:      models.RolePassenger,
				CompanyID: companyID,
			})
		}

		segments = append(segments, protocol.RideSegmentPassengersResponse{
			SegmentID: stage.ID, Origin: stage.Origin, Destination: stage.Destination,
			DepartureAt: stage.DepartureAt, ArrivalAt: stage.ArrivalAt, Passengers: passengers,
		})
	}

	return rideResponse(protocol.RidePassengersResponse{RideID: ride.ID, Segments: segments}, "Passageiros obtidos com sucesso.")
}

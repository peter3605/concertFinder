import CoreLocation
import Observation
import SwiftUI

/// One-shot CoreLocation wrapper.
///
/// "When in use" and a single fix, never continuous updates: the app needs to
/// know roughly where the user is once, and a background location capability
/// would be both unjustifiable at App Review and a battery cost for nothing.
@MainActor
@Observable
final class LocationProvider: NSObject, CLLocationManagerDelegate {
    enum Status: Equatable {
        case idle
        case requesting
        case denied
        case failed(String)
    }

    private(set) var status: Status = .idle

    private let manager = CLLocationManager()
    private var continuation: CheckedContinuation<CLLocationCoordinate2D, Error>?

    override init() {
        super.init()
        manager.delegate = self
        // City-level is all the server does anything with — it rounds saved
        // coordinates so a jittery GPS fix is one location rather than many.
        // Asking for best accuracy would spend battery to be discarded.
        manager.desiredAccuracy = kCLLocationAccuracyKilometer
    }

    func requestOnce() async throws -> CLLocationCoordinate2D {
        if manager.authorizationStatus == .denied || manager.authorizationStatus == .restricted {
            status = .denied
            throw APIError.unknown("Location access is off for ConcertFinder. You can turn it on in Settings, or type a city instead.")
        }
        status = .requesting
        return try await withCheckedThrowingContinuation { continuation in
            self.continuation = continuation
            if manager.authorizationStatus == .notDetermined {
                manager.requestWhenInUseAuthorization()
            } else {
                manager.requestLocation()
            }
        }
    }

    /// CLLocationManager is not Sendable, so the delegate parameter cannot
    /// cross into the main actor. The authorization status is a plain enum
    /// and can — everything past this point uses the instance's own manager.
    nonisolated func locationManagerDidChangeAuthorization(_ manager: CLLocationManager) {
        let authorization = manager.authorizationStatus
        MainActor.assumeIsolated {
            switch authorization {
            case .authorizedWhenInUse, .authorizedAlways:
                self.manager.requestLocation()
            case .denied, .restricted:
                status = .denied
                resume(throwing: APIError.unknown("Location access was declined. You can type a city instead."))
            case .notDetermined:
                break // Still waiting on the prompt.
            @unknown default:
                break
            }
        }
    }

    nonisolated func locationManager(_ manager: CLLocationManager, didUpdateLocations locations: [CLLocation]) {
        // The coordinate is a plain value; [CLLocation] is not Sendable, so
        // it is unwrapped before the hop rather than captured.
        let coordinate = locations.last?.coordinate
        MainActor.assumeIsolated {
            guard let coordinate else { return }
            status = .idle
            continuation?.resume(returning: coordinate)
            continuation = nil
        }
    }

    nonisolated func locationManager(_ manager: CLLocationManager, didFailWithError error: Error) {
        let description = error.localizedDescription
        MainActor.assumeIsolated {
            status = .failed(description)
            resume(throwing: APIError.unknown(description))
        }
    }

    private func resume(throwing error: Error) {
        continuation?.resume(throwing: error)
        continuation = nil
    }
}

@MainActor
@Observable
final class LocationModel {
    private(set) var location: UserLocation?
    private(set) var isSaving = false
    private(set) var error: APIError?

    var cityQuery = ""
    var radius: Double = 50

    /// Fired after the saved location actually changes.
    ///
    /// The feed reads `is_default` once, inside `start()`, which does not
    /// re-run when this pushed screen is popped — so the user did exactly what
    /// the "set your location" banner asked and came back to the same
    /// wrong-city results under the same banner. A closure rather than a
    /// reference to FeedModel, so this screen keeps knowing nothing about it.
    var onLocationChanged: (@MainActor () -> Void)?

    private let api: APIClient
    let provider = LocationProvider()

    init(api: APIClient) {
        self.api = api
    }

    func load() async {
        do {
            let current = try await api.location()
            location = current
            radius = Double(current.radiusMiles)
            // Do not prefill the field with a fallback city — it would read
            // as the user's own saved location.
            if !current.usesFallback, let name = current.displayName {
                cityQuery = name
            }
            error = nil
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
    }

    /// Two decimal places is roughly a kilometre, which is the resolution the
    /// feature actually uses.
    ///
    /// Not cosmetic and not redundant with `desiredAccuracy`: that limits
    /// what CoreLocation *spends* to get a fix, not what it hands back, and
    /// on a device with a recent GPS fix it hands back metres. The privacy
    /// manifest declares coarse location, so sending a precise coordinate
    /// would be collecting something the app says it does not. The server
    /// rounds too — this stops it being sent at all.
    /// `nonisolated` because `@MainActor` on the class covers its static
    /// members too, and this is a pure function the tests call directly.
    nonisolated static func coarse(_ value: Double) -> Double {
        (value * 100).rounded() / 100
    }

    func useCurrentLocation() async {
        isSaving = true
        defer { isSaving = false }
        do {
            let coordinate = try await provider.requestOnce()
            location = try await api.setLocation(
                latitude: Self.coarse(coordinate.latitude),
                longitude: Self.coarse(coordinate.longitude),
                radiusMiles: Int(radius)
            )
            cityQuery = location?.displayName ?? cityQuery
            error = nil
            onLocationChanged?()
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
    }

    /// The server geocodes the string — the app does not, so there is one
    /// geocoder, one rate limit, and one User-Agent honouring Nominatim's
    /// policy.
    func saveCity() async {
        let term = cityQuery.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !term.isEmpty else { return }
        isSaving = true
        defer { isSaving = false }
        do {
            location = try await api.setLocation(query: term, radiusMiles: Int(radius))
            error = nil
            onLocationChanged?()
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
    }

    func saveRadius() async {
        guard let current = location else { return }
        isSaving = true
        defer { isSaving = false }
        // Assigned only on success. `location = try?` wiped the saved location
        // to nil whenever the write failed, which the view reads as "you
        // haven't set a location".
        guard let updated = try? await api.setLocation(
            latitude: current.latitude,
            longitude: current.longitude,
            radiusMiles: Int(radius)
        ) else { return }
        location = updated
        // The radius is applied upstream at fetch time, so a wider one is a
        // different result set, not a different filter over the same one.
        onLocationChanged?()
    }

    /// Sign-out.
    func reset() {
        location = nil
        isSaving = false
        error = nil
        cityQuery = ""
        radius = 50
    }
}

struct LocationView: View {
    @Environment(LocationModel.self) private var model

    var body: some View {
        @Bindable var model = model

        Form {
            Section {
                if let location = model.location, !location.usesFallback {
                    LabeledContent("Current", value: location.displayName ?? "Saved location")
                } else {
                    Text("You haven't set a location, so we're showing shows near a default city.")
                        .foregroundStyle(.secondary)
                }
            }

            Section("Use my location") {
                Button {
                    Task { await model.useCurrentLocation() }
                } label: {
                    Label("Use current location", systemImage: "location")
                }
                .disabled(model.isSaving)
                if model.provider.status == .denied {
                    Text("Location access is off. Turn it on in Settings, or type a city below.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }

            Section("Or type a city") {
                TextField("City, state", text: $model.cityQuery)
                    .textInputAutocapitalization(.words)
                    .autocorrectionDisabled()
                    .onSubmit { Task { await model.saveCity() } }
                Button("Save city") { Task { await model.saveCity() } }
                    .disabled(model.cityQuery.isEmpty || model.isSaving)
            }

            Section {
                VStack(alignment: .leading) {
                    Text("Within \(Int(model.radius)) miles")
                    Slider(value: $model.radius, in: 10...200, step: 10) { editing in
                        if !editing { Task { await model.saveRadius() } }
                    }
                }
            } header: {
                Text("Search radius")
            } footer: {
                Text("A wider radius finds more shows but takes longer to scan.")
            }

            if let error = model.error {
                Section {
                    Text(error.userMessage).foregroundStyle(.secondary)
                }
            }
        }
        .navigationTitle("Location")
        .navigationBarTitleDisplayMode(.inline)
        .task { await model.load() }
    }
}

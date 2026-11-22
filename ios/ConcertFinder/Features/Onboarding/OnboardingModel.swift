import Foundation
import Observation

/// Decides whether a signed-in account must go through the welcome steps
/// before the tabs, and which steps.
///
/// The app's counterpart to web/src/pages/welcome.tsx, with the same rules:
///
///   name     Email sign-in never supplies one, and Apple shares it only the
///            first time and only if the person agrees. Account deletion
///            confirms against it, so an account without one cannot delete
///            itself.
///   email    Apple may hide the address. The digest and alerts need one.
///            Skippable: the feed works without it.
///   location Before picks on purpose. The first scan starts as soon as there
///            is something to scan, and with no location it would search the
///            deployment's default city.
///   picks    Artists or genres, at least one. With neither the server has
///            nothing to scan and answers `needs_picks` instead of queueing.
///
/// A Spotify account never sees this: its library is the feed, and it adds
/// picks from Settings. Everyone else comes here when they have no name or
/// nothing picked -- on sign-in, and again whenever the feed says
/// `needs_picks`, which is what happens if every pick is removed later.
@MainActor
@Observable
final class OnboardingModel {
    enum Step: String, CaseIterable, Sendable {
        case name, email, location, picks

        var title: String {
            switch self {
            case .name: "What should we call you?"
            case .email: "Where should we send updates?"
            case .location: "Where do you go to shows?"
            case .picks: "Who do you want to see?"
            }
        }
    }

    enum Phase: Equatable {
        /// Not yet asked for this session's account.
        case unknown
        case checking
        case active(steps: [Step], index: Int)
        case done
    }

    private(set) var phase: Phase = .unknown

    private let api: APIClient
    private let picks: PicksModel

    init(api: APIClient, picks: PicksModel) {
        self.api = api
        self.picks = picks
    }

    /// The steps an account still needs, or none. Pure so the rule can be
    /// tested without a server.
    nonisolated static func steps(
        hasSpotify: Bool,
        hasName: Bool,
        hasEmail: Bool,
        hasLocation: Bool,
        pickCount: Int
    ) -> [Step] {
        guard !hasSpotify else { return [] }
        // Only an account that is missing something essential is stopped;
        // one with a name and a pick goes straight to its feed even if it
        // never gave an address or a city -- those have their own prompts.
        guard !hasName || pickCount == 0 else { return [] }
        var steps: [Step] = []
        if !hasName { steps.append(.name) }
        if !hasEmail { steps.append(.email) }
        if !hasLocation { steps.append(.location) }
        steps.append(.picks)
        return steps
    }

    /// Run on sign-in and launch. Fails open: if the location or picks cannot
    /// be read, the account goes to its feed, which can show a cached
    /// snapshot offline -- a welcome screen that cannot save anything would
    /// be a dead end.
    func evaluate(_ me: Me) async {
        if case .active = phase { return }
        guard !me.hasSpotify else {
            phase = .done
            return
        }
        phase = .checking
        let location = try? await api.location()
        await picks.load()
        guard picks.isLoaded else {
            phase = .done
            return
        }
        let steps = Self.steps(
            hasSpotify: false,
            hasName: !me.displayName.isEmpty,
            hasEmail: me.hasEmail,
            hasLocation: location.map { !$0.usesFallback } ?? true,
            pickCount: picks.total
        )
        phase = steps.isEmpty ? .done : .active(steps: steps, index: 0)
    }

    /// The feed answered `needs_picks`: there is nothing to scan, so send the
    /// account back here rather than leave it on a list that never fills.
    func require(_ me: Me) async {
        if case .active = phase { return }
        phase = .unknown
        await evaluate(me)
    }

    var currentStep: Step? {
        guard case .active(let steps, let index) = phase, steps.indices.contains(index) else { return nil }
        return steps[index]
    }

    var progress: (index: Int, count: Int)? {
        guard case .active(let steps, let index) = phase else { return nil }
        return (index, steps.count)
    }

    var isLastStep: Bool {
        guard case .active(let steps, let index) = phase else { return false }
        return index == steps.count - 1
    }

    func advance() {
        guard case .active(let steps, let index) = phase else { return }
        phase = index + 1 < steps.count ? .active(steps: steps, index: index + 1) : .done
    }

    func reset() {
        phase = .unknown
    }
}

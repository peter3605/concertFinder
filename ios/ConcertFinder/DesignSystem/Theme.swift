import Foundation
import Observation
import SwiftUI

/// The user's appearance choice, mirroring the web client's three states
/// (`web/src/lib/theme.tsx`) so the two clients behave the same way.
///
/// `system` is the default and is not the same as `light`: it defers to the
/// device at render time, so flipping iOS into dark mode re-renders the app
/// without it storing anything.
enum ThemeChoice: String, CaseIterable, Identifiable, Sendable {
    case system
    case light
    case dark

    var id: String { rawValue }

    var label: String {
        switch self {
        case .system: "System"
        case .light: "Light"
        case .dark: "Dark"
        }
    }

    /// What to hand `.preferredColorScheme`. `nil` means "do not override",
    /// which is how SwiftUI spells "follow the system" -- the reason this is
    /// an optional rather than a two-case enum plus a flag.
    var colorScheme: ColorScheme? {
        switch self {
        case .system: nil
        case .light: .light
        case .dark: .dark
        }
    }
}

/// Persists the appearance choice.
///
/// The app's palette is built from semantic system colours, so dark mode was
/// always inherited from the device -- what was missing was a way to choose
/// it independently, which the web client has had. Nothing here re-themes
/// anything; it only decides whether to override the device.
@MainActor
@Observable
final class ThemeStore {
    /// Same key the web client uses in localStorage. Different storage, but
    /// there is no reason for the two to drift.
    private static let storageKey = "cf-theme"

    private let defaults: UserDefaults

    var choice: ThemeChoice {
        didSet {
            guard choice != oldValue else { return }
            defaults.set(choice.rawValue, forKey: Self.storageKey)
        }
    }

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        let stored = defaults.string(forKey: Self.storageKey)
        // An unrecognised value falls back to `system` rather than crashing
        // or sticking: this is read on every cold launch.
        choice = stored.flatMap(ThemeChoice.init(rawValue:)) ?? .system
    }
}

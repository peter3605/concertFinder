import Foundation
import Testing

@testable import ConcertFinder

@MainActor
struct ThemeStoreTests {

    private func scratchDefaults() -> UserDefaults {
        let suite = "ThemeStoreTests-\(UUID().uuidString)"
        let d = UserDefaults(suiteName: suite)!
        d.removePersistentDomain(forName: suite)
        return d
    }

    /// The app followed the device before this control existed, so anyone who
    /// never opens the setting must see exactly that.
    @Test func defaultsToSystem() {
        let store = ThemeStore(defaults: scratchDefaults())
        #expect(store.choice == .system)
        #expect(store.choice.colorScheme == nil)
    }

    @Test func choiceSurvivesRelaunch() {
        let defaults = scratchDefaults()

        ThemeStore(defaults: defaults).choice = .dark

        #expect(ThemeStore(defaults: defaults).choice == .dark)
    }

    /// A value written by a future build — or corrupted — must not stick the
    /// app in an unreadable state. This is read on every cold launch.
    @Test func unrecognisedStoredValueFallsBackToSystem() {
        let defaults = scratchDefaults()
        defaults.set("sepia", forKey: "cf-theme")

        #expect(ThemeStore(defaults: defaults).choice == .system)
    }

    /// `system` must stay distinct from `light`: it means "do not override",
    /// so a user flipping iOS into dark mode still gets a dark app.
    @Test func onlyExplicitChoicesOverrideTheDevice() {
        #expect(ThemeChoice.system.colorScheme == nil)
        #expect(ThemeChoice.light.colorScheme == .light)
        #expect(ThemeChoice.dark.colorScheme == .dark)
    }
}

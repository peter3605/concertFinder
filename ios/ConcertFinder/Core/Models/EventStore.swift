import Foundation

/// A loaded list of events that owns the save and subscribe state of the acts
/// in it.
///
/// `EventDetailView` is reachable from two lists — the feed and the Saved tab
/// — and each is backed by its own model holding its own copy of the events. A
/// detail screen has to read *and* write through the same one, or it reads a
/// collection that nothing it does can change. That is the bug this exists to
/// make unrepresentable: a card opened from Saved read `FeedModel`, fell
/// through to the immutable copy the list had pushed because the feed does not
/// hold that event, and wrote its save into `FeedModel` anyway — so the
/// request went out, the server agreed, and the bookmark never moved.
///
/// One object answering both halves is the whole point. Resolving an owner for
/// the read and picking a model for the write as two separate decisions is the
/// same bug with more steps in front of it.
@MainActor
protocol EventStore: AnyObject {
    var events: [Event] { get }
    func toggleSave(act: Act) async
    func toggleSubscribe(act: Act) async
}

extension EventStore {
    /// Events are identified across lists by `eventKey`, never by index — the
    /// two models sort and filter independently.
    func event(withKey key: String) -> Event? {
        events.first { $0.eventKey == key }
    }
}

enum EventStores {
    /// Which loaded list owns an event, given the ones a screen can see.
    ///
    /// Split out of the view and left free of view state so the rule can be
    /// tested directly — the screen that applies it holds its stores in
    /// `@Environment` and resolves them inside a private computed property,
    /// neither of which a test can reach.
    ///
    /// Ordered rather than scored: a show can legitimately be in more than one
    /// list, and every list that holds it answers correctly, so the first wins.
    /// `nil` is the honest answer when none of them does — there is no
    /// observable copy to flip, and a caller that quietly substituted one of
    /// the stores anyway would be back to writing where nobody is reading.
    @MainActor
    static func owner(of key: String, among candidates: [any EventStore]) -> (any EventStore)? {
        candidates.first { $0.event(withKey: key) != nil }
    }
}

struct User {
    var name: String
    func greet() -> String { return "hi " + name }
    func get(key: String) -> String { return key }
}
class Session {
    var user: User?
    func start() {}
    func get(key: String) -> String? { return nil }
    func get(index: Int) -> String? { return nil }
}
extension User { func shout() -> String { return greet().uppercased() } }
func makeSession() -> Session { return Session() }

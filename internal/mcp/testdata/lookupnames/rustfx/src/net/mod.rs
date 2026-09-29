pub mod client;
pub fn connect(addr: &str) -> client::Client { client::Client::new(addr) }

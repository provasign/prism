pub struct Client { addr: String }
impl Client {
    pub fn new(addr: &str) -> Self { Client { addr: addr.to_string() } }
    pub fn send(&self, b: &[u8]) -> usize { b.len() }
    pub fn get(&self, path: &str) -> String { format!("{}{}", self.addr, path) }
}

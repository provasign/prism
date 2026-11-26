pub mod net;
pub struct Config { pub name: String, pub retries: u32 }
impl Config {
    pub fn new(name: &str) -> Self { Config { name: name.to_string(), retries: 3 } }
    pub fn name(&self) -> &str { &self.name }
    pub fn validate(&self) -> bool { !self.name.is_empty() }
}
pub trait Store { fn get(&self, key: &str) -> Option<String>; }
pub struct MemStore { items: Vec<(String,String)> }
impl Store for MemStore {
    fn get(&self, key: &str) -> Option<String> { self.items.iter().find(|(k,_)| k==key).map(|(_,v)| v.clone()) }
}
pub struct Cache<T> { inner: Vec<T> }
impl<T: Clone> Cache<T> { pub fn get(&self, i: usize) -> Option<T> { self.inner.get(i).cloned() } }
pub fn parse_config(s: &str) -> Config { Config::new(s) }

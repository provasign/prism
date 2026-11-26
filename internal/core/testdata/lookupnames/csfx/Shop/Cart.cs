namespace Acme.Shop
{
    public class Cart
    {
        public int Count { get; set; }
        public decimal Total;
        public void Add(Item item) { Count++; }
        public void Add(Item item, int qty) { Count += qty; }
        public decimal Get(int i) { return 0; }
        public class Line { public void Render() {} }
    }
    public class Item { public string Name { get; set; } public decimal Get(int i) { return 1; } }
    public interface IRepo<T> { T Find(int id); }
    public class Repo<T> : IRepo<T> { public T Find(int id) { return default(T); } }
}

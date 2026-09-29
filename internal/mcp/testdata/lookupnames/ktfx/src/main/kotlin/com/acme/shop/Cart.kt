package com.acme.shop
class Cart(val owner: String) {
    val size: Int get() = 0
    fun add(item: Item) {}
    fun add(item: Item, qty: Int) {}
    fun get(i: Int): Item? = null
    class Line { fun render() {} }
    companion object { fun empty(): Cart = Cart("") }
}
data class Item(val name: String) { fun get(i: Int): String = name }
fun topLevelHelper(x: Int): Int = x + 1
object Registry { fun register(c: Cart) {} }

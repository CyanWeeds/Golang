package main

import "fmt"

// 常量定义后无法修改
const pi = 3.1415926

// 批量声明常量
const (
	s1 = 1
	s2 = 2
	s3 = 3
)

// 批量声明常量，如果值相同，可以不写值。
const (
	s4 = 10
	s5
	s6
)

// iota 在const关键字出现时将被重置为0。const中每新增*一行*常量声明将使iota计数一次(iota可理解为const语句块中的行索引)。 使用iota能简化定义，在定义枚举时很有用。
const (
	a1 = iota // 0
	a2 = iota // 1
	a3        // 2
)
const (
	a4 = iota // 0
	a5        // 1
	_         // 2
	a6        // 3
)
const (
	b1 = iota // 0
	b2 = 100  // 100
	b3        // 100
	b4        // 100
)
const (
	b5 = iota // 0
	b6 = 100  // 100
	b7 = iota // 2
	b8        // 3
)

// 定义数量级
const (
	_  = iota             // 0
	KB = 1 << (10 * iota) // 1往左移10位，也就是2的10次方，1024
	MB = 1 << (10 * iota) // 1往左移20位，也就是2的20次方，1 048 576
	GB = 1 << (10 * iota)
	TB = 1 << (10 * iota)
)

func main() {
	fmt.Println(pi, s1, s2, s3, s4, s5, s6)
	fmt.Println(a1, a2, a3, a4, a5, a6)
	fmt.Println(b1, b2, b3, b4, b5, b6, b7, b8)
	fmt.Println(KB, MB, GB, TB)
}

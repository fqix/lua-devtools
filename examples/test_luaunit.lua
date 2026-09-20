local lu = require('luaunit')

TestExample = {}

function TestExample:setUp()
    self.value = 41
    print('SETUP')
end

function TestExample:testSelected()
    local actual = self.value + 1
    lu.assertEquals(actual, 42)
    print('SELECTED')
end

function TestExample:testOther()
    print('OTHER')
    lu.assertTrue(true)
end

function TestExample:tearDown()
    print('TEARDOWN')
end

os.exit(lu.LuaUnit.run())
